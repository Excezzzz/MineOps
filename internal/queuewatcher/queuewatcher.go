// Package queuewatcher — background Aternos queue confirmer.
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

type Watcher struct {
	db       *database.DB
	managers *aternos.Registry
	dash     *dashboard.Dashboard

	mu     sync.Mutex
	active map[int64]bool
}

func New(db *database.DB, managers *aternos.Registry, dash *dashboard.Dashboard) *Watcher {
	return &Watcher{
		db:       db,
		managers: managers,
		dash:     dash,
		active:   make(map[int64]bool),
	}
}

func (w *Watcher) IsWatching(serverID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active[serverID]
}

// Start launches the single background queue watcher (if not running yet):
// panel polling every 15 seconds, auto-confirm on lang=="confirm",
// plus a blind Confirm every 45 seconds until the server comes online.
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

// Rescan starts watching all active servers that are already in the
// Aternos queue (survived a bot restart). Each server is checked against
// the panel once; if it is in the starting/queue state — a watcher
// takes over.
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
	// inactiveSince — when the server was last in an "inactive" state
	// (not in queue, not loading). If it still has not started within
	// idleTimeout — the launch is considered failed and the owner is notified.
	var inactiveSince time.Time

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		status, err := manager.FetchInfo(ctx, serverID)
		cancel()

		if err != nil {
			log.Printf("queuewatcher: server %d: poll failed: %v", serverID, err)
			failures++
			if failures >= maxFailures {
				w.notifyOwner(b, ownerID, serverID, fmt.Sprintf("queue poll failed: %v", err))
				break
			}
			time.Sleep(pollInterval)
			continue
		}
		failures = 0

		serverLang := strings.ToLower(status.Lang)
		queueValue := parseQueue(status.Queue)

		// Server came online (status==1 or lang on/online) — notify chats
		// and finish watching.
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

		// active — the server is in a state where launch is still in progress
		// (queue running, server loading, or awaiting confirmation).
		active := false

		// Aternos asks for launch confirmation — confirm right away.
		if strings.Contains(serverLang, "confirm") {
			active = true
			// Fresh context: the previous one was already canceled (cancel() after FetchInfo),
			// otherwise ConfirmServer fails immediately with context canceled.
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

		// Blind Confirm every 45 seconds: Aternos does not always update
		// lang in time, and the confirm API is idempotent — an extra request does no harm.
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
			w.notifyOwner(b, ownerID, serverID, "server did not start (20 min outside queue)")
			w.refreshDashboard(b, serverID)
			return
		}
		time.Sleep(pollInterval)
	}

	w.dash.ClearQueuePosition(serverID)
	w.refreshDashboard(b, serverID)
}

// parseQueue — queue position from lastStatus['queue'].
//
// The value can be: a number (including -1 — awaiting confirmation), a numeric
// string, or a dict like {"count": N, "position": M}. None — queue undefined.
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

func (w *Watcher) ownerLang(ownerID int64) string {
	if o, _ := w.db.GetOwner(ownerID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

func (w *Watcher) chatLang(chatID int64) string {
	if o, _ := w.db.GetChatOwner(chatID); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

// Temporary notification, deleted after 60 seconds.
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
