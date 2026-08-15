// Package queuewatcher — наблюдатель очереди запуска Aternos (порт queue_watcher.py).
//
// После server.start(), если сервер попал в очередь, фоновая задача каждые
// 15 секунд опрашивает статус панели и, когда сервер доходит до состояния
// «ожидает подтверждения» (lang содержит "confirm"), автоматически вызывает
// confirm. По ходу обновляет дашборды с позицией в очереди.
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
)

const (
	pollInterval    = 15 * time.Second
	watchTimeout    = 15 * time.Minute
	maxFailures     = 5
	dashboardPeriod = 30 * time.Second
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

// Start запускает фонового наблюдателя очереди (если ещё не запущен).
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
	deadline := time.Now().Add(watchTimeout)
	failures := 0
	lastDashUpdate := time.Time{}

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		status, err := manager.FetchInfo(ctx, serverID)
		cancel()

		if err != nil {
			log.Printf("queuewatcher: сервер %d: опрос не удался: %v", serverID, err)
			failures++
			if failures >= maxFailures {
				w.notifyOwner(b, ownerID, serverID, fmt.Sprintf("опрос очереди не удался: %v", err))
				break
			}
			time.Sleep(pollInterval)
			continue
		}
		failures = 0

		serverLang := status.Lang
		queueValue := parseQueue(status.Queue)
		log.Printf("queuewatcher: сервер %d: status=%d lang=%q queue=%v",
			serverID, status.Status, serverLang, status.Queue)

		// Aternos просит подтверждение запуска — ТОЛЬКО при явном сигнале.
		if strings.Contains(strings.ToLower(serverLang), "confirm") {
			if err := manager.ConfirmServer(ctx, serverID); err != nil {
				log.Printf("queuewatcher: сервер %d: confirm не удался (lang=%q): %v — ждём итерации",
					serverID, serverLang, err)
			} else {
				log.Printf("queuewatcher: сервер %d: запуск подтверждён (lang=%q)", serverID, serverLang)
				w.dash.SetQueuePosition(serverID, 0)
				w.refreshDashboard(b, serverID)
			}
			time.Sleep(pollInterval)
			continue
		}

		switch serverLang {
		case "waiting", "preparing":
			if queueValue > 0 {
				w.dash.SetQueuePosition(serverID, queueValue)
				log.Printf("queuewatcher: сервер %d: очередь идёт, позиция %d", serverID, queueValue)
			}
			now := time.Now()
			if now.Sub(lastDashUpdate) >= dashboardPeriod {
				w.refreshDashboard(b, serverID)
				lastDashUpdate = now
			}
		case "loading", "starting":
			log.Printf("queuewatcher: сервер %d: грузится (lang=%q), ждём запуска", serverID, serverLang)
		case "on", "online":
			log.Printf("queuewatcher: сервер %d: онлайн, watcher завершён", serverID)
			if p := toIntAny(status.Port); p > 0 {
				_ = w.db.SetServerPort(serverID, p)
			}
			w.dash.ClearQueuePosition(serverID)
			w.refreshDashboard(b, serverID)
			return
		default:
			log.Printf("queuewatcher: сервер %d: состояние lang=%q, ждём итерации", serverID, serverLang)
		}
		time.Sleep(pollInterval)
	}

	w.dash.ClearQueuePosition(serverID)
	w.notifyOwner(b, ownerID, serverID, "таймаут 15 минут: очередь не была пройдена")
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

func toIntAny(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
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

func (w *Watcher) notifyOwner(b *tele.Bot, ownerID, serverID int64, reason string) {
	server, err := w.db.GetServer(serverID)
	name := fmt.Sprintf("ID %d", serverID)
	if err == nil && server != nil {
		name = server.DisplayName
	}
	_, err = b.Send(&tele.Chat{ID: ownerID},
		fmt.Sprintf("⚠️ <b>Автоподтверждение запуска не сработало:</b>\n🖥 %s\n%s\nПроверьте Aternos вручную.",
			html.EscapeString(name), reason))
	if err != nil {
		log.Printf("queuewatcher: не удалось уведомить владельца %d о сервере %d: %v", ownerID, serverID, err)
	}
	_ = w.db.LogAction(ownerID, "auto_confirm_failed", fmt.Sprintf("%s: %s", name, reason), 0, 0, serverID)
}