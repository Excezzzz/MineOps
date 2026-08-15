package telegram

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/aternos"
	"mineops/internal/config"
	"mineops/internal/dashboard"
	"mineops/internal/database"
	"mineops/internal/queuewatcher"
)

// Bot — главный объект бота (диспетчер, мидлвари, состояние).
type Bot struct {
	b        *tele.Bot
	cfg      *config.Config
	db       *database.DB
	managers *aternos.Registry
	dash     *dashboard.Dashboard
	watcher  *queuewatcher.Watcher
	fsm      *FSM

	mu              sync.Mutex
	lastAccessReq   map[int64]time.Time // cooldown запросов доступа (5 мин)
	lastErrNotified time.Time           // анти-спам уведомлений суперадмину
}

const (
	accessReqCooldown = 5 * time.Minute
	errNotifyMinGap   = time.Minute
)

// NewBot создаёт и настраивает telebot.
func NewBot(cfg *config.Config, db *database.DB, managers *aternos.Registry,
	dash *dashboard.Dashboard, watcher *queuewatcher.Watcher) (*Bot, error) {

	bot := &Bot{
		cfg:           cfg,
		db:            db,
		managers:      managers,
		dash:          dash,
		watcher:       watcher,
		fsm:           NewFSM(),
		lastAccessReq: make(map[int64]time.Time),
	}

	pref := tele.Settings{
		Token:     cfg.BotToken,
		Poller:    &tele.LongPoller{Timeout: 10 * time.Second},
		ParseMode: tele.ModeHTML,
		OnError:   bot.onError,
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}
	bot.b = b

	// Мидлвари: восстановление после паники, фаервол, регистрация участников.
	b.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("PANIC in handler: %v\n%s", r, debug.Stack())
					bot.notifyOwnerCritical(fmt.Sprintf("%v", r), debug.Stack())
				}
			}()
			if c.Message() != nil {
				if !bot.firewall(c) {
					return nil
				}
				bot.registerUser(c)
			}
			return next(c)
		}
	})

	bot.registerHandlers()
	bot.setCommands()
	return bot, nil
}

// Bot возвращает telebot (для планировщика).
func (bot *Bot) Bot() *tele.Bot { return bot.b }

// Start запускает поллинг (блокирует).
func (bot *Bot) Start() {
	bot.b.Start()
}

// ------------------------------------------------------------------ //
// регистрация хэндлеров
// ------------------------------------------------------------------ //

func (bot *Bot) registerHandlers() {
	b := bot.b

	// ЛС: /start, /panel, /help, /set_session, /emergency, /announce
	b.Handle("/start", bot.cmdStart)
	b.Handle("/panel", bot.cmdPanel)
	b.Handle("/help", bot.cmdHelp)
	b.Handle("/set_session", bot.cmdSetSession)
	b.Handle("/emergency", bot.cmdEmergency)
	b.Handle("/announce", bot.cmdAnnounce)

	// Группы: /link, /unlink, /status, /run
	b.Handle("/link", bot.cmdLink)
	b.Handle("/unlink", bot.cmdUnlink)
	b.Handle("/status", bot.cmdStatus)
	b.Handle("/run", bot.cmdRun)

	// Текст (FSM: онбординг, обновление куки)
	b.Handle(tele.OnText, bot.onText)

	// Все callback-кнопки (единый диспетчер с префиксной маршрутизацией)
	b.Handle(tele.OnCallback, bot.onCallback)

	// Заглушка: любые прочие события не падают в ошибку
	b.Handle(tele.OnEdited, func(c tele.Context) error { return nil })
}

func (bot *Bot) setCommands() {
	_ = bot.b.SetCommands(
		tele.Command{Text: "start", Description: "Панель владельца / онбординг"},
		tele.Command{Text: "panel", Description: "Панель владельца"},
		tele.Command{Text: "help", Description: "Справка"},
		tele.Command{Text: "status", Description: "Статус серверов (группа / ЛС)"},
		tele.Command{Text: "run", Description: "Запустить серверы (группа / ЛС)"},
		tele.Command{Text: "link", Description: "Привязать чат (в группе, владелец)"},
		tele.Command{Text: "unlink", Description: "Отвязать чат (в группе, владелец)"},
		tele.Command{Text: "set_session", Description: "Обновить куку Aternos"},
		tele.Command{Text: "emergency", Description: "Локдаун вкл/выкл"},
	)
}

// ------------------------------------------------------------------ //
// мидлвари
// ------------------------------------------------------------------ //

// firewall — аналог FirewallMiddleware: ЛС пропускает; группы — только
// привязанные к владельцу или если отправитель — владелец; прочее игнор.
func (bot *Bot) firewall(c tele.Context) bool {
	if c.Message() == nil {
		return true
	}
	m := c.Message()
	if m.Chat == nil {
		return false
	}
	switch m.Chat.Type {
	case tele.ChatPrivate:
		return true
	case tele.ChatGroup, tele.ChatSuperGroup:
		owner, err := bot.db.GetChatOwner(m.Chat.ID)
		if err == nil && owner != nil {
			return true
		}
		if m.Sender != nil {
			if isOwner, _ := bot.db.IsOwner(m.Sender.ID); isOwner {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// registerUser — аналог RegisterMiddleware: участники привязанных чатов
// автоматически попадают в users (для проверки доступа).
func (bot *Bot) registerUser(c tele.Context) {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return
	}
	if m.Chat.Type != tele.ChatGroup && m.Chat.Type != tele.ChatSuperGroup {
		return
	}
	owner, err := bot.db.GetChatOwner(m.Chat.ID)
	if err != nil || owner == nil {
		return
	}
	_ = bot.db.UpsertChatUser(m.Chat.ID, m.Sender.ID, m.Sender.Username, m.Sender.FirstName+" "+m.Sender.LastName)
}

// ------------------------------------------------------------------ //
// общий обработчик ошибок
// ------------------------------------------------------------------ //

func (bot *Bot) onError(err error, c tele.Context) {
	log.Printf("ERROR: %v", err)
	if c != nil {
		log.Printf("  context: update=%d chat=%d", c.Update().ID, c.Chat().ID)
	}
	// Мусорные ошибки Telegram API (спам кнопками, rate limit, повторные
	// правки) владельцу не отправляются — это не ошибки системы.
	if isNoiseError(err) {
		return
	}
	bot.notifyOwnerError(err.Error())
}

// isNoiseError — ошибки, которые возникают при спаме кнопками / кликах
// по дашборду в группе и НЕ являются реальными сбоями бота.
func isNoiseError(err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "429"), strings.Contains(msg, "too many requests"):
		return true
	case strings.Contains(msg, "query is too old"):
		return true
	case strings.Contains(msg, "message is not modified"):
		return true
	case strings.Contains(msg, "callback_query"):
		return true
	}
	return false
}

func isPanicError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "panic:")
}

// notifyOwnerError — отправка ошибки Владельцу в ЛС.
// Анти-спам: не чаще одного сообщения в минуту.
func (bot *Bot) notifyOwnerError(text string) {
	if bot.cfg.SuperAdminID <= 0 {
		return
	}
	bot.mu.Lock()
	now := time.Now()
	if now.Sub(bot.lastErrNotified) < errNotifyMinGap {
		bot.mu.Unlock()
		return
	}
	bot.lastErrNotified = now
	bot.mu.Unlock()

	msg := "⚠️ <b>Ошибка бота:</b>\n<code>" + escapeHTML(text) + "</code>"
	if len(msg) > 3500 {
		msg = msg[:3500]
	}
	_, err := bot.b.Send(&tele.Chat{ID: bot.cfg.SuperAdminID}, msg)
	if err != nil {
		log.Printf("не удалось уведомить суперадмина: %v", err)
	}
}

// notifyOwnerCritical — уведомление Владельца о критических падениях
// (panic/exception). Анти-спам: не чаще одного сообщения в минуту.
func (bot *Bot) notifyOwnerCritical(reason string, stack []byte) {
	if bot.cfg.SuperAdminID <= 0 {
		return
	}
	bot.mu.Lock()
	now := time.Now()
	if now.Sub(bot.lastErrNotified) < errNotifyMinGap {
		bot.mu.Unlock()
		return
	}
	bot.lastErrNotified = now
	bot.mu.Unlock()

	text := "🚨 <b>Паника в боте:</b>\n<code>" + escapeHTML(reason) + "</code>"
	if len(stack) > 0 {
		text += "\n<pre>" + escapeHTML(string(stack)) + "</pre>"
	}
	if len(text) > 3500 {
		text = text[:3500]
	}
	_, err := bot.b.Send(&tele.Chat{ID: bot.cfg.SuperAdminID}, text)
	if err != nil {
		log.Printf("не удалось уведомить суперадмина: %v", err)
	}
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// SafeCall выполняет хэндлер с защитой от паники.
func (bot *Bot) SafeCall(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn()
}

// ------------------------------------------------------------------ //
// единый диспетчер callback-кнопок
// ------------------------------------------------------------------ //

func (bot *Bot) onCallback(c tele.Context) error {
	return bot.SafeCall(func() error {
		cb := c.Callback()
		if cb == nil {
			return nil
		}
		parts := splitCb(cb.Data)
		if len(parts) == 0 || parts[0] == "" {
			return nil
		}
		switch parts[0] {
		case cbPanel:
			return bot.cbPanel(c, parts)
		case cbPanelServer:
			return bot.cbPanelServer(c, parts)
		case cbPanelAction:
			return bot.cbPanelAction(c, parts)
		case cbPanelChat:
			return bot.cbPanelChat(c, parts)
		case cbPanelChatS:
			return bot.cbPanelChatServer(c, parts)
		case cbUsersPage:
			return bot.cbUsersPage(c, parts)
		case cbOwnerSet:
			return bot.cbOwnerSettings(c, parts)
		case cbDeleteAcc:
			return bot.cbDeleteAccount(c, parts)
		case cbServer:
			return bot.cbServerAction(c, parts)
		case cbRefreshDash:
			return bot.cbRefreshDashboard(c)
		case cbReqAccess:
			return bot.cbRequestAccess(c, parts)
		case cbApproveAcc:
			return bot.cbApproveAccess(c, parts)
		case cbOnboarding:
			return bot.cbOnboarding(c, parts)
		case cbNoop:
			return c.Respond(&tele.CallbackResponse{})
		}
		log.Printf("callback: неизвестный префикс: %q", cb.Data)
		return c.Respond(&tele.CallbackResponse{})
	})
}

// ------------------------------------------------------------------ //
// вспомогательные
// ------------------------------------------------------------------ //

func (bot *Bot) answer(c tele.Context, text string, showAlert bool) {
	_ = c.Respond(&tele.CallbackResponse{Text: text, ShowAlert: showAlert})
}

// edit — безопасный edit_text (не падает на «message is not modified»).
func (bot *Bot) edit(msg tele.Editable, text string, kb *tele.ReplyMarkup) error {
	// ParseMode задаём явно: переданный SendOptions иначе перетирает
	// глобальный ParseMode (HTML) пустым значением — теги показываются как текст.
	_, err := bot.b.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML, ReplyMarkup: kb})
	if err == tele.ErrSameMessageContent {
		return nil
	}
	if err != nil {
		log.Printf("edit failed: %v", err)
	}
	return err
}

// decodeCookie — значение куки из текста сообщения (обрезает префиксы).
func decodeCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.LastIndex(raw, "="); idx >= 0 {
		return strings.TrimSpace(raw[idx+1:])
	}
	return raw
}

func chatIDStr(id int64) string { return strconv.FormatInt(id, 10) }

// withTimeout — контекст с таймаутом для сетевых операций с Aternos.
func (bot *Bot) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}