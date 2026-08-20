// Package dashboard — живой дашборд статусов (порт services/dashboard.py).
//
// Каждые 30 секунд опрашиваются статусы серверов владельцев и обновляется
// ЕДИНЫЙ закреплённый дашборд в каждом привязанном чате. Если сообщение
// удалено или закрепа ещё нет — дашборд отправляется и закрепляется заново.
package dashboard

import (
	"context"
	"fmt"
	"html"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/aternos"
	"mineops/internal/database"
	"mineops/internal/i18n"
	"mineops/internal/mcsrvstat"
	"mineops/internal/util"
)

// DashServer — сервер со статусом (для рендера дашборда).
type DashServer struct {
	ID            int64
	DisplayName   string
	ServerIP      string
	IsOnline      bool
	PlayersOnline int
	PlayersMax    int
	PlayerList    []string
	Version       string
	Port          int
	Starting      bool // в процессе запуска/в очереди — показывать «Подтвердить»
}

// KBFactory создаёт клавиатуру дашборда для чата (инжектится из telegram,
// чтобы избежать циклического импорта).
type KBFactory func(servers []DashServer, chatID int64, lang string) *tele.ReplyMarkup

// Dashboard — состояние дашбордов и кешей статусов.
type Dashboard struct {
	db       *database.DB
	managers *aternos.Registry
	kb       KBFactory

	mu             sync.Mutex
	startingUntil  time.Time
	queuePositions map[int64]int
	panelCache     map[int64]panelEntry
	panelFailUntil map[int64]time.Time
	pinNoticeSent  map[int64]bool
	pinPending     map[int64]bool
	sessionBroken  map[int64]bool
	lastOnline     map[int64]bool
	lastPlayers    map[int64]map[string]bool
}

type panelEntry struct {
	at   time.Time
	info *aternos.ServerInfo
}

const (
	panelTTL     = 60 * time.Second
	panelFailCD  = 5 * time.Minute
	playersLimit = 20
)

// New создаёт Dashboard. kb — фабрика клавиатуры дашборда.
func New(db *database.DB, managers *aternos.Registry, kb KBFactory) *Dashboard {
	return &Dashboard{
		db:             db,
		managers:       managers,
		kb:             kb,
		queuePositions: make(map[int64]int),
		panelCache:     make(map[int64]panelEntry),
		panelFailUntil: make(map[int64]time.Time),
		pinNoticeSent:  make(map[int64]bool),
		pinPending:     make(map[int64]bool),
		sessionBroken:  make(map[int64]bool),
		lastOnline:     make(map[int64]bool),
		lastPlayers:    make(map[int64]map[string]bool),
	}
}

// chatLang возвращает язык интерфейса для чата (язык владельца чата).
func (d *Dashboard) chatLang(chatID int64) string {
	if o, _ := d.db.GetChatOwner(chatID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// ownerLang возвращает язык владельца по user_id.
func (d *Dashboard) ownerLang(ownerID int64) string {
	if o, _ := d.db.GetOwner(ownerID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// ------------------------------------------------------------------ //
// состояние запуска / очереди
// ------------------------------------------------------------------ //

// MarkAsStarting помечает серверы как запускающиеся на `seconds` секунд.
func (d *Dashboard) MarkAsStarting(seconds int) {
	d.mu.Lock()
	d.startingUntil = time.Now().Add(time.Duration(seconds) * time.Second)
	d.mu.Unlock()
}

// ClearStarting сбрасывает пометку «запускается».
func (d *Dashboard) ClearStarting() {
	d.mu.Lock()
	d.startingUntil = time.Time{}
	d.mu.Unlock()
}

// IsStarting — True, если серверы ещё в состоянии «запускается».
func (d *Dashboard) IsStarting() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Now().Before(d.startingUntil)
}

// SetQueuePosition запоминает позицию сервера в очереди запуска.
func (d *Dashboard) SetQueuePosition(serverID int64, position int) {
	d.mu.Lock()
	d.queuePositions[serverID] = position
	d.mu.Unlock()
}

// ClearQueuePosition убирает позицию очереди.
func (d *Dashboard) ClearQueuePosition(serverID int64) {
	d.mu.Lock()
	delete(d.queuePositions, serverID)
	d.mu.Unlock()
}

func (d *Dashboard) queuePosition(serverID int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queuePositions[serverID]
}

// ------------------------------------------------------------------ //
// рендер
// ------------------------------------------------------------------ //

// FormatDashboardText формирует единый дашборд (HTML).
func (d *Dashboard) FormatDashboardText(servers []DashServer, lang string) string {
	if len(servers) == 0 {
		return i18n.T(lang, "dash_no_servers", nowLabel(lang))
	}

	blocks := make([]string, 0, len(servers))
	for _, s := range servers {
		ip := s.ServerIP
		if ip == "" {
			ip = i18n.T(lang, "not_set")
		}
		name := s.DisplayName
		if name == "" {
			name = ip
		}

		var statusLine string
		if s.IsOnline {
			d.ClearStarting()
			d.ClearQueuePosition(s.ID)
			statusLine = i18n.T(lang, "dash_status_online", html.EscapeString(name))
		} else if pos := d.queuePosition(s.ID); pos > 0 {
			statusLine = i18n.T(lang, "dash_status_queue", html.EscapeString(name), pos)
		} else if d.IsStarting() {
			statusLine = i18n.T(lang, "dash_status_starting", html.EscapeString(name))
		} else {
			statusLine = i18n.T(lang, "dash_status_offline", html.EscapeString(name))
		}

		lines := []string{statusLine, i18n.T(lang, "dash_ip", html.EscapeString(ip))}
		if s.IsOnline {
			if len(s.PlayerList) > 0 {
				names := make([]string, 0, len(s.PlayerList))
				for _, n := range s.PlayerList {
					names = append(names, "• "+html.EscapeString(n))
				}
				if len(names) > playersLimit {
					names = names[:playersLimit]
				}
				lines = append(lines, i18n.T(lang, "dash_players_list",
					s.PlayersOnline, s.PlayersMax, strings.Join(names, "\n")))
			} else {
				lines = append(lines, i18n.T(lang, "dash_players_num", s.PlayersOnline, s.PlayersMax))
			}
		}
		version := s.Version
		if version == "" {
			version = i18n.T(lang, "unknown")
		}
		lines = append(lines, i18n.T(lang, "dash_version", html.EscapeString(version)))
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n") + "\n\n" + nowLabel(lang)
}

// nowLabel — «🕐 Обновлено: HH:MM:SS TZ» (UTC или TZ из ENV).
func nowLabel(lang string) string {
	tz := os.Getenv("TZ")
	loc := time.UTC
	label := "UTC"
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
			label = tz
		}
	}
	return i18n.T(lang, "dash_timestamp", time.Now().In(loc).Format("15:04:05"), label)
}

// ------------------------------------------------------------------ //
// панель Aternos (авторитетный источник)
// ------------------------------------------------------------------ //

func (d *Dashboard) ensureServerPort(ctx context.Context, ownerID, serverID int64) int {
	port, err := d.db.GetServerPort(serverID)
	if err == nil && port > 0 && port != 25565 {
		return port
	}
	// Берём из кеша панели (TTL 60с), а не прямым запросом — иначе каждый
	// тик дашборда долбит /server на Aternos.
	info := d.getPanelCached(ctx, ownerID, serverID)
	if info != nil {
		p := util.ToInt(info.Port)
		if p > 0 && p != 25565 {
			_ = d.db.SetServerPort(serverID, p)
			return p
		}
	}
	return 0
}

func (d *Dashboard) getPanelCached(ctx context.Context, ownerID, serverID int64) *aternos.ServerInfo {
	now := time.Now()
	d.mu.Lock()
	cached, hasCache := d.panelCache[serverID]
	failUntil, failing := d.panelFailUntil[serverID]
	d.mu.Unlock()

	if hasCache && now.Sub(cached.at) < panelTTL {
		return cached.info
	}
	if failing && now.Before(failUntil) {
		if hasCache {
			return cached.info // устаревший кеш лучше, чем врущий legacy-пинг
		}
		return nil
	}

	info, err := d.managers.For(ownerID).FetchInfo(ctx, serverID)
	if err != nil {
		d.mu.Lock()
		d.panelFailUntil[serverID] = now.Add(panelFailCD)
		d.mu.Unlock()
		if hasCache {
			return cached.info
		}
		return nil
	}
	d.mu.Lock()
	d.panelCache[serverID] = panelEntry{at: now, info: info}
	delete(d.panelFailUntil, serverID)
	d.mu.Unlock()
	return info
}

func panelToStatus(server *database.Server, panel *aternos.ServerInfo) DashServer {
	online := panel.Status == 1
	names := panel.PlayerList
	port := util.ToInt(panel.Port)
	var players, slots int
	if online {
		players = util.ToInt(panel.Players)
		slots = util.ToInt(panel.Slots)
	}
	version := panel.Version
	return DashServer{
		ID:            server.ID,
		DisplayName:   server.DisplayName,
		ServerIP:      server.ServerIP,
		IsOnline:      online,
		PlayersOnline: players,
		PlayersMax:    slots,
		PlayerList:    names,
		Version:       version,
		Port:          port,
	}
}

// startingState — сервер в процессе запуска (запускается или стоит в очереди),
// когда пользователю нужна кнопка «Подтвердить».
func (d *Dashboard) startingState(serverID int64, online bool) bool {
	if online {
		return false
	}
	return d.IsStarting() || d.queuePosition(serverID) > 0
}

// GetAuthoritativeStatus — панель Aternos, иначе честный mcsrvstat.
func (d *Dashboard) GetAuthoritativeStatus(ctx context.Context, ownerID int64, server *database.Server) DashServer {
	if panel := d.getPanelCached(ctx, ownerID, server.ID); panel != nil {
		st := panelToStatus(server, panel)
		st.Starting = d.startingState(server.ID, st.IsOnline)
		return st
	}
	port := d.ensureServerPort(ctx, ownerID, server.ID)
	status := mcsrvstat.GetServerStatus(server.ServerIP, port, false)
	return DashServer{
		ID:            server.ID,
		DisplayName:   server.DisplayName,
		ServerIP:      server.ServerIP,
		IsOnline:      status.IsOnline,
		PlayersOnline: status.PlayersOnline,
		PlayersMax:    status.PlayersMax,
		PlayerList:    status.PlayerList,
		Version:       status.Version,
		Port:          status.Port,
		Starting:      d.startingState(server.ID, status.IsOnline),
	}
}

// ------------------------------------------------------------------ //
// закрепление
// ------------------------------------------------------------------ //

func (d *Dashboard) tryPin(b *tele.Bot, chatID int64, msg *tele.Message) bool {
	if err := b.Pin(msg, tele.Silent); err != nil {
		log.Printf("dashboard: chat %d: failed to pin (%v)", chatID, err)
		return false
	}
	return true
}

// pinIfNeeded закрепляет дашборд, если он ещё не закреплён (самовосстановление).
func (d *Dashboard) pinIfNeeded(b *tele.Bot, chatID int64, messageID int) bool {
	chat, err := b.ChatByID(chatID)
	if err == nil && chat.PinnedMessage != nil && chat.PinnedMessage.ID == messageID {
		return true
	}
	return d.tryPin(b, chatID, &tele.Message{ID: messageID, Chat: &tele.Chat{ID: chatID}})
}

func (d *Dashboard) renderDashboard(ctx context.Context, b *tele.Bot, chatID int64, text string, kb *tele.ReplyMarkup) {
	pinnedMsgID, err := d.db.GetChatPinnedMsg(chatID)
	if err != nil {
		return
	}

	if pinnedMsgID > 0 {
		msg := &tele.StoredMessage{MessageID: strconv.Itoa(int(pinnedMsgID)), ChatID: chatID}
		_, err := b.Edit(msg, text, &tele.SendOptions{ParseMode: tele.ModeHTML, ReplyMarkup: kb})
		if err == nil {
			d.pinState(chatID, !d.pinIfNeeded(b, chatID, int(pinnedMsgID)))
			return
		}
		if err != tele.ErrSameMessageContent {
			log.Printf("dashboard: chat %d: edit failed (%v), will re-send", chatID, err)
		} else {
			d.pinState(chatID, !d.pinIfNeeded(b, chatID, int(pinnedMsgID)))
			return // контент не изменился — дашборд актуален
		}
		// Падаем вниз и пересоздаём сообщение.
	}

	msg, err := b.Send(&tele.Chat{ID: chatID}, text, &tele.SendOptions{ParseMode: tele.ModeHTML, ReplyMarkup: kb})
	if err != nil {
		log.Printf("dashboard: chat %d: cannot send (%v)", chatID, err)
		return
	}
	pinnedOK := d.tryPin(b, chatID, msg)

	// id сохраняется всегда: при неудачном пине следующий тик правит новое сообщение.
	_ = d.db.SetChatPinnedMsg(chatID, int64(msg.ID))

	if pinnedOK {
		d.mu.Lock()
		delete(d.pinPending, chatID)
		delete(d.pinNoticeSent, chatID)
		d.mu.Unlock()
		log.Printf("dashboard: chat %d: created and pinned (msg %d)", chatID, msg.ID)
	} else {
		d.mu.Lock()
		d.pinPending[chatID] = true
		if !d.pinNoticeSent[chatID] {
			d.pinNoticeSent[chatID] = true
			lang := d.chatLang(chatID)
			go func() {
				_, err := b.Send(&tele.Chat{ID: chatID}, i18n.T(lang, "pin_help"))
				if err != nil {
					log.Printf("dashboard: chat %d: pin hint not sent: %v", chatID, err)
				}
			}()
		}
		d.mu.Unlock()
	}
}

func (d *Dashboard) pinState(chatID int64, pending bool) {
	d.mu.Lock()
	if pending {
		d.pinPending[chatID] = true
	} else {
		delete(d.pinPending, chatID)
	}
	d.mu.Unlock()
}

// ------------------------------------------------------------------ //
// обновление дашбордов
// ------------------------------------------------------------------ //

func (d *Dashboard) updateChatDashboard(ctx context.Context, b *tele.Bot, chatID int64) {
	servers, err := d.db.GetChatServers(chatID)
	if err != nil || len(servers) == 0 {
		return // владелец ещё не привязал серверы — дашборд не нужен
	}

	for _, s := range servers {
		d.ensureServerPort(ctx, s.OwnerID, s.ID)
	}
	statuses := make(map[int64]DashServer, len(servers))
	for _, s := range servers {
		statuses[s.ID] = d.GetAuthoritativeStatus(ctx, s.OwnerID, s)
	}

	merged := make([]DashServer, 0, len(servers))
	for _, s := range servers {
		st := statuses[s.ID]
		knownPort, _ := d.db.GetServerPort(s.ID)
		if st.IsOnline && st.Port > 0 && st.Port != knownPort {
			_ = d.db.SetServerPort(s.ID, st.Port)
		}
		merged = append(merged, st)
	}

	lang := d.chatLang(chatID)
	text := d.FormatDashboardText(merged, lang)
	if d.kb != nil {
		d.renderDashboard(ctx, b, chatID, text, d.kb(merged, chatID, lang))
		return
	}
	d.renderDashboard(ctx, b, chatID, text, nil)
}

func (d *Dashboard) updateOwnerPMDashboard(ctx context.Context, b *tele.Bot, owner *database.Owner, merged []DashServer) {
	lang := owner.Lang
	if lang == "" {
		lang = "ru"
	}
	text := d.FormatDashboardText(merged, lang)
	if len(merged) == 0 {
		text = i18n.T(lang, "dash_pm_no_servers")
	}

	pmPinned, err := d.db.GetOwnerPmPinned(owner.UserID)
	if err != nil {
		return
	}
	if pmPinned > 0 {
		msg := &tele.StoredMessage{MessageID: strconv.Itoa(int(pmPinned)), ChatID: owner.UserID}
		_, err := b.Edit(msg, text)
		if err == nil || err == tele.ErrSameMessageContent {
			return // контент не изменился или успешно обновлён
		}
		log.Printf("dashboard: owner %d: edit PM failed (%v), will re-send", owner.UserID, err)
	}

	msg, err := b.Send(&tele.Chat{ID: owner.UserID}, text)
	if err != nil {
		log.Printf("dashboard: owner %d: PM dashboard not sent: %v", owner.UserID, err)
		return
	}
	pinnedOK := true
	if err := b.Pin(msg, tele.Silent); err != nil {
		log.Printf("dashboard: owner %d: PM dashboard not pinned: %v", owner.UserID, err)
		pinnedOK = false
	}
	_ = d.db.SetOwnerPmPinned(owner.UserID, int64(msg.ID))
	if pinnedOK {
		log.Printf("dashboard: owner %d: PM dashboard created and pinned (msg %d)", owner.UserID, msg.ID)
	}
}

// UpdateDashboards обновляет дашборды всех владельцев в их привязанных чатах.
func (d *Dashboard) UpdateDashboards(ctx context.Context, b *tele.Bot) {
	owners, err := d.db.GetAllOwners()
	if err != nil {
		log.Printf("dashboard: owners not fetched: %v", err)
		return
	}
	for _, owner := range owners {
		servers, err := d.db.GetServersByOwner(owner.UserID)
		if err != nil {
			continue
		}
		statuses := make(map[int64]DashServer, len(servers))
		for _, s := range servers {
			status := d.GetAuthoritativeStatus(ctx, owner.UserID, s)
			statuses[s.ID] = status
			if status.IsOnline {
				d.ClearStarting()
				d.ClearQueuePosition(s.ID)
			}
			knownPort, _ := d.db.GetServerPort(s.ID)
			if status.IsOnline && status.Port > 0 && status.Port != knownPort {
				_ = d.db.SetServerPort(s.ID, status.Port)
			}
			// Уведомление при переходе сервера в оффлайн (только смена статуса).
			d.mu.Lock()
			wasOnline := d.lastOnline[s.ID]
			if wasOnline != status.IsOnline {
				d.lastOnline[s.ID] = status.IsOnline
				if !status.IsOnline {
					// Сервер ушёл оффлайн: сбрасываем список игроков, чтобы
					// не слать «вышел» по всем, когда сервер вернётся онлайн.
					delete(d.lastPlayers, s.ID)
				}
				d.mu.Unlock()
				if wasOnline {
					log.Printf("dashboard: server %d (%s) went offline", s.ID, s.ServerIP)
					d.notifyServerOffline(b, s)
				} else {
					log.Printf("dashboard: server %d (%s) came online", s.ID, s.ServerIP)
				}
			} else {
				d.mu.Unlock()
			}

			// Уведомления о входе/выходе игроков (по списку из mcsrvstat).
			if status.IsOnline && status.PlayersOnline > 0 {
				d.notifyPlayers(b, s, status.PlayerList)
			}
		}
		chats, err := d.db.GetChatsByOwner(owner.UserID)
		if err != nil {
			continue
		}
		for _, chat := range chats {
			d.updateChatDashboard(ctx, b, chat.ChatID)
		}
		pmMerged := make([]DashServer, 0, len(servers))
		for _, s := range servers {
			pmMerged = append(pmMerged, statuses[s.ID])
		}
		d.updateOwnerPMDashboard(ctx, b, owner, pmMerged)
	}
}

// notifyServerOffline шлёт во все привязанные чаты сервера временное
// уведомление «сервер ушёл в оффлайн» (удаляется через 60 секунд).
func (d *Dashboard) notifyServerOffline(b *tele.Bot, s *database.Server) {
	name := s.DisplayName
	if name == "" {
		name = fmt.Sprintf("ID %d", s.ID)
	}
	chats, err := d.db.GetChatsForServer(s.ID)
	if err != nil {
		return
	}
	for _, ch := range chats {
		text := i18n.T(d.chatLang(ch.ChatID), "srv_offline_notify", html.EscapeString(name))
		msg, err := b.Send(&tele.Chat{ID: ch.ChatID}, text)
		if err != nil {
			log.Printf("dashboard: offline notification for server %d not sent to chat %d: %v",
				s.ID, ch.ChatID, err)
			continue
		}
		go func(m *tele.Message) {
			time.Sleep(60 * time.Second)
			_ = b.Delete(m)
		}(msg)
	}
}

// notifyPlayers шлёт в чаты сервера уведомления о входе/выходе игроков
// (дельта против lastPlayers). До 3 игроков — по одному сообщению на игрока
// («🟢 X зашёл на сервер»), больше — одно объединённое сообщение списком.
// Каждое уведомление временное: удаляется через 45 секунд.
func (d *Dashboard) notifyPlayers(b *tele.Bot, s *database.Server, current []string) {
	d.mu.Lock()
	prev := d.lastPlayers[s.ID]
	if prev == nil {
		prev = map[string]bool{}
	}
	cur := map[string]bool{}
	for _, n := range current {
		cur[n] = true
	}

	var joined, left []string
	for _, n := range current {
		if !prev[n] {
			joined = append(joined, n)
		}
	}
	for n := range prev {
		if !cur[n] {
			left = append(left, n)
		}
	}
	if len(joined) == 0 && len(left) == 0 {
		d.mu.Unlock()
		return
	}
	d.lastPlayers[s.ID] = cur
	d.mu.Unlock()

	name := s.DisplayName
	if name == "" {
		name = s.ServerIP
	}
	if name == "" {
		name = fmt.Sprintf("ID %d", s.ID)
	}
	escapedName := html.EscapeString(name)

	chats, err := d.db.GetChatsForServer(s.ID)
	if err != nil {
		return
	}
	for _, ch := range chats {
		lang := d.chatLang(ch.ChatID)
		var texts []string
		if len(joined) > 0 {
			if len(joined) <= 3 {
				for _, n := range joined {
					texts = append(texts, i18n.T(lang, "qw_join", html.EscapeString(n), escapedName))
				}
			} else {
				texts = append(texts, i18n.T(lang, "players_join_many", joinNames(joined), escapedName))
			}
		}
		if len(left) > 0 {
			if len(left) <= 3 {
				for _, n := range left {
					texts = append(texts, i18n.T(lang, "qw_leave", html.EscapeString(n), escapedName))
				}
			} else {
				texts = append(texts, i18n.T(lang, "players_leave_many", joinNames(left), escapedName))
			}
		}
		for _, text := range texts {
			msg, err := b.Send(&tele.Chat{ID: ch.ChatID}, text)
			if err != nil {
				log.Printf("dashboard: player notification for server %d not sent to chat %d: %v",
					s.ID, ch.ChatID, err)
				continue
			}
			go func(m *tele.Message) {
				time.Sleep(45 * time.Second)
				_ = b.Delete(m)
			}(msg)
		}
	}
}

// joinNames экранирует и склеивает имена игроков через запятую.
func joinNames(names []string) string {
	escaped := make([]string, 0, len(names))
	for _, n := range names {
		escaped = append(escaped, html.EscapeString(n))
	}
	return strings.Join(escaped, ", ")
}

// UpdateChatsDashboards обновляет дашборды в конкретных чатах.
func (d *Dashboard) UpdateChatsDashboards(ctx context.Context, b *tele.Bot, chatIDs []int64) {
	for _, chatID := range chatIDs {
		d.updateChatDashboard(ctx, b, chatID)
	}
}

// ------------------------------------------------------------------ //
// проверка сессий
// ------------------------------------------------------------------ //

// CheckAllSessions проверяет куки всех владельцев; при просрочке шлёт в ЛС.
func (d *Dashboard) CheckAllSessions(ctx context.Context, b *tele.Bot) {
	owners, err := d.db.GetAllOwners()
	if err != nil {
		return
	}
	for _, owner := range owners {
		uid := owner.UserID
		err := d.managers.For(uid).CheckSession(ctx)
		if err != nil {
			if isCloudflareError(err) {
				continue // временный бан запросов — не про куку
			}
			d.mu.Lock()
			notified := d.sessionBroken[uid]
			d.mu.Unlock()
			if notified {
				continue
			}
			d.mu.Lock()
			d.sessionBroken[uid] = true
			d.mu.Unlock()
			_ = d.db.LogAction(uid, "session_expired", "кука Aternos просрочена", 0, 0, 0)
			lang := owner.Lang
			if lang == "" {
				lang = "ru"
			}
			_, err := b.Send(&tele.Chat{ID: uid}, i18n.T(lang, "cookie_expired_pm"))
			if err != nil {
				log.Printf("dashboard: owner %d: cookie notification not sent: %v", uid, err)
			}
			continue
		}
		d.mu.Lock()
		delete(d.sessionBroken, uid)
		d.mu.Unlock()
	}
}

func isCloudflareError(err error) bool {
	return strings.Contains(err.Error(), "Cloudflare")
}

// BroadcastMessage рассылает текст в чаты (ошибки отдельных чатов не фатальны).
func (d *Dashboard) BroadcastMessage(b *tele.Bot, chatIDs []int64, text string) {
	for _, chatID := range chatIDs {
		_, err := b.Send(&tele.Chat{ID: chatID}, text)
		if err != nil {
			log.Printf("dashboard: broadcast: chat %d: %v", chatID, err)
		}
	}
}

// SortedChatIDs сортирует id чатов (детерминированный порядок обновления).
func SortedChatIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
