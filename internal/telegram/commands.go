package telegram

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/database"
	"mineops/internal/i18n"
)

func (bot *Bot) cmdPing(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		lang := bot.uiLang(c)
		latency := time.Since(time.Unix(m.Unixtime, 0))
		ms := int(latency.Milliseconds())
		if ms < 1 {
			ms = 1
		}
		msg, err := bot.b.Send(m.Chat, i18n.T(lang, "ping", ms))
		bot.autoDeleteInGroup(c, msg, 30)
		return err
	})
}

func (bot *Bot) cmdInfo(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		uid := m.Sender.ID
		chatID := m.Chat.ID
		lang := bot.uiLang(c)

		var ownerID int64
		var servers []*database.Server

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "need_onboarding"))
				return err
			}
			ownerID = uid
			servers, _ = bot.db.GetActiveServersByOwner(uid)
		} else if bot.isGroup(c) {
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_access_chat"))
				return err
			}
			owner, err := bot.db.GetChatOwner(chatID)
			if err != nil || owner == nil {
				return nil
			}
			ownerID = owner.UserID
			servers, _ = bot.db.GetChatServers(chatID)
		} else {
			return nil
		}

		if len(servers) == 0 {
			msg, err := bot.b.Send(m.Chat, i18n.T(lang, "no_servers"))
			bot.autoDeleteInGroup(c, msg, 30)
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		blocks := make([]string, 0, len(servers))
		for _, s := range servers {
			st := bot.dash.GetAuthoritativeStatus(ctx, ownerID, s)
			name := s.DisplayName
			if name == "" {
				name = s.ServerIP
			}
			ip := s.ServerIP
			if ip == "" {
				ip = i18n.T(lang, "not_set")
			}
			statusLine := i18n.T(lang, "dash_status_offline", html.EscapeString(name))
			if st.IsOnline {
				statusLine = i18n.T(lang, "dash_status_online", html.EscapeString(name))
			}
			version := st.Version
			if version == "" {
				version = i18n.T(lang, "unknown")
			}
			blocks = append(blocks, strings.Join([]string{
				"🖥 <b>" + html.EscapeString(name) + "</b>",
				i18n.T(lang, "dash_ip", html.EscapeString(ip)),
				statusLine,
				i18n.T(lang, "dash_players_num", st.PlayersOnline, st.PlayersMax),
				i18n.T(lang, "dash_version", html.EscapeString(version)),
			}, "\n"))
		}
		msg, err := bot.b.Send(m.Chat, strings.Join(blocks, "\n\n"))
		bot.autoDeleteInGroup(c, msg, 30)
		return err
	})
}

func (bot *Bot) cmdStats(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isPrivate(c) {
			return nil
		}
		uid := m.Sender.ID
		lang := bot.uiLang(c)
		isOwner, _ := bot.db.IsOwner(uid)
		if !isOwner {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "need_onboarding"))
			return err
		}

		total, _ := bot.db.GetStartCount(uid)
		week, _ := bot.db.GetStartCountSince(uid, time.Now().AddDate(0, 0, -7))
		month, _ := bot.db.GetStartCountSince(uid, time.Now().AddDate(0, 0, -30))

		lines := []string{
			i18n.T(lang, "stats_title"),
			i18n.T(lang, "stats_total", total),
			i18n.T(lang, "stats_7d", week),
			i18n.T(lang, "stats_30d", month),
		}

		recent, _ := bot.db.GetRecentStarts(uid, 5)
		if len(recent) > 0 {
			lines = append(lines, i18n.T(lang, "stats_recent_starts"))
			for _, e := range recent {
				serverName := fmt.Sprintf("ID %d", e.ServerID.Int64)
				if s, err := bot.db.GetServer(e.ServerID.Int64); err == nil && s != nil && s.DisplayName != "" {
					serverName = s.DisplayName
				}
				lines = append(lines, "• "+html.EscapeString(serverName)+" — "+timeAgo(e.CreatedAt, lang))
			}
		}
		_, err := bot.b.Send(m.Chat, strings.Join(lines, "\n"))
		return err
	})
}

// Форматы: «только что», «5м», «2ч», «вчера», «3д».
func timeAgo(ts string, lang string) string {
	t, err := time.Parse("2006-01-02T15:04:05", ts)
	if err != nil {
		return ts
	}
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return i18n.T(lang, "time_ago_just_now")
	case diff < time.Hour:
		return i18n.T(lang, "time_ago_min", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return i18n.T(lang, "time_ago_hour", int(diff.Hours()))
	case diff < 48*time.Hour:
		return i18n.T(lang, "time_ago_yesterday")
	default:
		return i18n.T(lang, "time_ago_days", int(diff.Hours()/24))
	}
}

func (bot *Bot) cmdSchedule(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isPrivate(c) {
			return nil
		}
		uid := m.Sender.ID
		lang := bot.uiLang(c)
		isOwner, _ := bot.db.IsOwner(uid)
		if !isOwner {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "need_onboarding"))
			return err
		}
		owner, _ := bot.db.GetOwner(uid)

		args := strings.Fields(m.Text)
		current := i18n.T(lang, "off")
		if owner != nil && owner.ScheduleTime != "" {
			current = owner.ScheduleTime
		}

		if len(args) < 2 {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "sched_usage", current))
			return err
		}
		cmd := args[1]
		if cmd == "off" {
			_ = bot.db.SetOwnerSchedule(uid, "", false)
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "sched_off"))
			return err
		}
		if _, err := time.Parse("15:04", cmd); err != nil {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "sched_bad_time"))
			return err
		}
		once := len(args) >= 3 && args[2] == "once"
		_ = bot.db.SetOwnerSchedule(uid, cmd, once)
		text := i18n.T(lang, "sched_on", cmd)
		if once {
			text = i18n.T(lang, "sched_on_once", cmd)
		}
		_, err := bot.b.Send(m.Chat, text)
		return err
	})
}

// Однократное расписание сбрасывается после запуска; защита от повтора в течение суток — в памяти бота.
func (bot *Bot) CheckSchedule() {
	owners, err := bot.db.GetScheduledOwners()
	if err != nil {
		log.Printf("schedule: scheduled owners not fetched: %v", err)
		return
	}
	now := time.Now()
	hhmm := now.Format("15:04")
	today := now.Format("2006-01-02")
	for _, o := range owners {
		if o.ScheduleTime != hhmm {
			continue
		}
		bot.schedMu.Lock()
		if bot.lastSchedFire[o.UserID] == today {
			bot.schedMu.Unlock()
			continue
		}
		bot.lastSchedFire[o.UserID] = today
		bot.schedMu.Unlock()
		go bot.fireSchedule(o)
	}
}

func (bot *Bot) fireSchedule(o *database.Owner) {
	servers, err := bot.db.GetActiveServersByOwner(o.UserID)
	if err != nil {
		return
	}
	if len(servers) == 0 {
		return
	}
	var started []string
	chatIDs := []int64{}
	for _, s := range servers {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		text, err := bot.managers.For(o.UserID).StartServer(ctx, s.ID)
		cancel()
		if err != nil {
			log.Printf("schedule: server %d of owner %d failed to start: %v", s.ID, o.UserID, err)
			continue
		}
		_ = text
		started = append(started, s.DisplayName)
		bot.watcher.Start(bot.b, o.UserID, s.ID)
	}
	if len(started) > 0 {
		bot.dash.MarkAsStarting(300)
		chats, _ := bot.db.GetChatsByOwner(o.UserID)
		for _, ch := range chats {
			chatIDs = append(chatIDs, ch.ChatID)
		}
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)
	}
	if o.ScheduleOnce {
		_ = bot.db.SetOwnerSchedule(o.UserID, "", false)
	}
	if len(started) > 0 {
		_, _ = bot.b.Send(&tele.Chat{ID: o.UserID},
			i18n.T(o.Lang, "sched_fired", strings.Join(started, ", ")))
	}
}

// Для ЛС — язык владельца (или отправителя), для группы — язык владельца
// чата; фолбэк — детект по LanguageCode.
func (bot *Bot) uiLang(c tele.Context) string {
	if c == nil {
		return "ru"
	}
	var lang string
	if cb := c.Callback(); cb != nil {
		if cb.Message != nil && cb.Message.Chat != nil && cb.Message.Chat.Type != tele.ChatPrivate {
			if o, _ := bot.db.GetChatOwner(cb.Message.Chat.ID); o != nil {
				lang = o.Lang
			}
		} else if cb.Sender != nil {
			if o, _ := bot.db.GetOwner(cb.Sender.ID); o != nil {
				lang = o.Lang
			}
		}
	} else if m := c.Message(); m != nil && m.Sender != nil {
		if m.Chat != nil && m.Chat.Type != tele.ChatPrivate {
			if o, _ := bot.db.GetChatOwner(m.Chat.ID); o != nil {
				lang = o.Lang
			}
		} else {
			if o, _ := bot.db.GetOwner(m.Sender.ID); o != nil {
				lang = o.Lang
			}
		}
	}
	if lang == "" {
		lang = i18n.Detect(c.Sender().LanguageCode)
	}
	return lang
}

func (bot *Bot) ownerLang(uid int64) string {
	if o, _ := bot.db.GetOwner(uid); o != nil && o.Lang != "" {
		return o.Lang
	}
	return "ru"
}

func (bot *Bot) userLang(uid int64) string {
	return bot.ownerLang(uid)
}

func (bot *Bot) lockdownMsg(c tele.Context) string {
	return i18n.T(bot.uiLang(c), "lockdown_blocked")
}
