// Package queuewatcher — наблюдатель очереди запуска Aternos (порт queue_watcher.py).
//
// После server.start() фоновая задача каждые 15 секунд опрашивает статус
// панели и автоматически подтверждает запуск: по явному сигналу lang=="confirm"
// и дополнительно «вслепую» каждые 45 секунд (confirm идемпотентен). Когда
// сервер выходит онлайн — наблюдатель уведомляет чаты и завершается.
package queuewatcher

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/aternos"
	"mineops/internal/dashboard"
	"mineops/internal/database"
	"mineops/internal/i18n"
	"mineops/internal/util"
)

const (
	pollInterval         = 15 * time.Second
	idleTimeout          = 20 * time.Minute
	maxFailures          = 5
	dashboardPeriod      = 30 * time.Second
	blindConfirmInterval = 45 * time.Second
)

// Watcher — реестр активных наблюдателей.
type Watcher struct {
	db       *database.DB
	managers *aternos.Registry
	dash     *dashboard.Dashboard

	mu     sync.Mutex
	active map[int64]bool
}

// New создаёт Watcher.
func New(db *database.DB, managers *aternos.Registry, dash *dashboard.Dashboard) *Watcher {
	return &Watcher{
		db:       db,
		managers: managers,
		dash:     dash,
		active:   make(map[int64]bool),
	}
}

// IsWatching — идёт ли наблюдение за сервером.
func (w *Watcher) IsWatching(serverID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active[serverID]
}

// Start запускает ЕДИНЫЙ фоновый наблюдатель очереди (если ещё не запущен):
// опрос панели каждые 15 секунд, автоподтверждение при lang=="confirm"
// плюс слепой Confirm каждые 45 секунд, пока сервер не вышел онлайн.
func (w *Watcher) Start(b *tele.Bot, ownerID, serverID int64) {
	w.mu.Lock()
	if w.active[serverID] {
		w.mu.Unlock()
		return
	}
	w.active[serverID] = true
	w.mu.Unlock()
	go w.watchLoop(b, ownerID, serverID)
}

// Rescan запускает наблюдение за всеми активными серверами, которые уже
// находятся в очереди Aternos (пережили перезапуск бота). Каждый сервер
// проверяется панелью один раз; если он в состоянии запуска/очереди —
// за ним начинает следить watcher.
func (w *Watcher) Rescan(b *tele.Bot) {
	owners, err := w.db.GetAllOwners()
	if err != nil {
		log.Printf("queuewatcher: rescan: owners not fetched: %v", err)
		return
	}
	for _, owner := range owners {
		servers, err := w.db.GetActiveServersByOwner(owner.UserID)
		if err != nil {
			continue
		}
		manager := w.managers.For(owner.UserID)
		for _, s := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			info, err := manager.FetchInfo(ctx, s.ID)
			cancel()
			if err != nil {
				continue
			}
			lang := strings.ToLower(info.Lang)
			if lang == "" || lang == "off" || lang == "offline" || lang == "stop" || lang == "stopped" {
				continue
			}
			w.Start(b, owner.UserID, s.ID)
		}
	}
}

func (w *Watcher) watchLoop(b *tele.Bot, ownerID, serverID int64) {
	defer func() {
		w.mu.Lock()
		delete(w.active, serverID)
		w.mu.Unlock()
		if r := recover(); r != nil {
			log.Printf("queue watcher %d: panic: %v", serverID, r)
		}
	}()

	manager := w.managers.For(ownerID)
	failures := 0
	lastDashUpdate := time.Time{}
	lastBlindConfirm := time.Time{}
	// inactiveSince — когда сервер последний раз был в «неактивном» состоянии
	// (не в очереди и не грузится). Если он так и не запустился в течение
	// idleTimeout — считаем запуск проваленным и уведомляем владельца.
	var inactiveSince time.Time

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		status, err := manager.FetchInfo(ctx, serverID)
		cancel()

		if err != nil {
			log.Printf("queuewatcher: server %d: poll failed: %v", serverID, err)
			failures++
			if failures >= maxFailures {
				w.notifyOwner(b, ownerID, serverID, fmt.Sprintf("опрос очереди не удался: %v", err))
				break
			}
			time.Sleep(pollInterval)
			continue
		}
		failures = 0

		serverLang := strings.ToLower(status.Lang)
		queueValue := parseQueue(status.Queue)

		// Сервер вышел онлайн (status==1 или lang on/online) — уведомляем чаты
		// и завершаем наблюдение.
		if status.Status == 1 || serverLang == "on" || serverLang == "online" {
			log.Printf("queuewatcher: server %d: online, watcher finished", serverID)
			if p := util.ToInt(status.Port); p > 0 {
				_ = w.db.SetServerPort(serverID, p)
			}
			w.dash.ClearQueuePosition(serverID)
			w.refreshDashboard(b, serverID)
			w.notifyServerOnline(b, serverID, status)
			return
		}

		// active — сервер в состоянии, когда запуск ещё в процессе
		// (очередь идёт, сервер грузится или просит подтверждение).
		active := false

		// Aternos просит подтверждение запуска — подтверждаем сразу.
		if strings.Contains(serverLang, "confirm") {
			active = true
			// Свежий контекст: предыдущий уже отменён (cancel() после FetchInfo),
			// иначе ConfirmServer сразу упадёт с context canceled.
			cctx, ccancel := context.WithTimeout(context.Background(), 45*time.Second)
			err := manager.ConfirmServer(cctx, serverID)
			ccancel()
			lastBlindConfirm = time.Now()
			if err != nil {
				log.Printf("queuewatcher: server %d: confirm failed (lang=%q): %v — waiting for next iteration",
					serverID, serverLang, err)
			} else {
				log.Printf("queuewatcher: server %d: start confirmed (lang=%q)", serverID, serverLang)
				w.dash.SetQueuePosition(serverID, 0)
				w.refreshDashboard(b, serverID)
			}
			time.Sleep(pollInterval)
			continue
		}

		// Слепой Confirm каждые 45 секунд: Aternos не всегда успевает
		// обновить lang, а confirm API идемпотентен — лишний запрос не вредит.
		if time.Since(lastBlindConfirm) >= blindConfirmInterval {
			cctx, ccancel := context.WithTimeout(context.Background(), 45*time.Second)
			_ = manager.ConfirmServer(cctx, serverID)
			ccancel()
			lastBlindConfirm = time.Now()
			log.Printf("queuewatcher: server %d: blind confirm sent", serverID)
		}

		switch serverLang {
		case "waiting", "preparing":
			active = true
			if queueValue > 0 {
				w.dash.SetQueuePosition(serverID, queueValue)
				log.Printf("queuewatcher: server %d: queue in progress, position %d", serverID, queueValue)
			}
			now := time.Now()
			if now.Sub(lastDashUpdate) >= dashboardPeriod {
				w.refreshDashboard(b, serverID)
				lastDashUpdate = now
			}
		case "loading", "starting":
			active = true
			log.Printf("queuewatcher: server %d: loading (lang=%q), waiting for start", serverID, serverLang)
		default:
			log.Printf("queuewatcher: server %d: state lang=%q, waiting for next iteration", serverID, serverLang)
		}

		if active {
			inactiveSince = time.Time{}
		} else if inactiveSince.IsZero() {
			inactiveSince = time.Now()
		} else if time.Since(inactiveSince) >= idleTimeout {
			w.dash.ClearQueuePosition(serverID)
			w.notifyOwner(b, ownerID, serverID, "сервер не запустился (20 минут вне очереди)")
			w.refreshDashboard(b, serverID)
			return
		}
		time.Sleep(pollInterval)
	}

	w.dash.ClearQueuePosition(serverID)
	w.refreshDashboard(b, serverID)
}

// parseQueue — позиция в очереди из lastStatus['queue'].
//
// Значение может быть: числом (в т.ч. -1 — ждёт подтверждения), числовой
// строкой или dict вида {"count": N, "position": M}. None — очередь не определена.
func parseQueue(raw any) int {
	switch t := raw.(type) {
	case nil:
		return 0
	case string:
		if t == "" {
			return 0
		}
		return parseInt(t)
	case bool:
		return 0
	case float64:
		n := int(t)
		if n < 0 {
			return 0
		}
		return n
	case int:
		if t < 0 {
			return 0
		}
		return t
	case int64:
		n := int(t)
		if n < 0 {
			return 0
		}
		return n
	case map[string]any:
		for _, key := range []string{"position", "count"} {
			if v, ok := t[key]; ok {
				if n := parseIntFrom(v); n > 0 {
					return n
				}
			}
		}
		return 0
	}
	return 0
}

func parseInt(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	if n < 0 {
		return 0
	}
	return n
}

func parseIntFrom(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		return parseInt(t)
	}
	return 0
}

func (w *Watcher) refreshDashboard(b *tele.Bot, serverID int64) {
	chats, err := w.db.GetChatsForServer(serverID)
	if err != nil {
		return
	}
	if len(chats) > 0 {
		w.dash.UpdateDashboards(context.Background(), b)
	}
}

// ownerLang возвращает язык владельца по user_id.
func (w *Watcher) ownerLang(ownerID int64) string {
	if o, _ := w.db.GetOwner(ownerID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// chatLang возвращает язык интерфейса чата (язык владельца чата).
func (w *Watcher) chatLang(chatID int64) string {
	if o, _ := w.db.GetChatOwner(chatID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// notifyServerOnline шлёт во все привязанные чаты сервера временное
// уведомление «сервер запущен» (удаляется через 60 секунд).
func (w *Watcher) notifyServerOnline(b *tele.Bot, serverID int64, status *aternos.ServerInfo) {
	server, err := w.db.GetServer(serverID)
	if err != nil || server == nil {
		return
	}
	name := server.DisplayName
	if name == "" {
		name = fmt.Sprintf("ID %d", serverID)
	}
	ip := server.ServerIP
	if ip == "" {
		ip = util.ToStr(status.IP)
	}
	if ip == "" {
		ip = i18n.T("ru", "not_set")
	}
	chats, err := w.db.GetChatsForServer(serverID)
	if err != nil {
		return
	}
	for _, ch := range chats {
		lang := w.chatLang(ch.ChatID)
		text := i18n.T(lang, "qw_online", html.EscapeString(name), html.EscapeString(ip))
		msg, err := b.Send(&tele.Chat{ID: ch.ChatID}, text)
		if err != nil {
			log.Printf("queuewatcher: online notification for server %d not sent to chat %d: %v",
				serverID, ch.ChatID, err)
			continue
		}
		go func(m *tele.Message) {
			time.Sleep(60 * time.Second)
			_ = b.Delete(m)
		}(msg)
	}
}

func (w *Watcher) notifyOwner(b *tele.Bot, ownerID, serverID int64, reason string) {
	server, err := w.db.GetServer(serverID)
	name := fmt.Sprintf("ID %d", serverID)
	if err == nil && server != nil {
		name = server.DisplayName
	}
	lang := w.ownerLang(ownerID)
	_, err = b.Send(&tele.Chat{ID: ownerID},
		i18n.T(lang, "qw_failed", html.EscapeString(name), reason))
	if err != nil {
		log.Printf("queuewatcher: failed to notify owner %d about server %d: %v", ownerID, serverID, err)
	}
	_ = w.db.LogAction(ownerID, "auto_confirm_failed", fmt.Sprintf("%s: %s", name, reason), 0, 0, serverID)
}
