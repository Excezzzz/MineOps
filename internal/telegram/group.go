package telegram

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/dashboard"
	"mineops/internal/database"
	"mineops/internal/i18n"
)

// ------------------------------------------------------------------ //
// /link, /unlink, /status, /help (группы)
// ------------------------------------------------------------------ //

func (bot *Bot) isGroup(c tele.Context) bool {
	if c.Message() == nil || c.Message().Chat == nil {
		return false
	}
	t := c.Message().Chat.Type
	return t == tele.ChatGroup || t == tele.ChatSuperGroup
}

func (bot *Bot) cmdLink(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isGroup(c) {
			return nil
		}
		uid := m.Sender.ID
		isOwner, _ := bot.db.IsOwner(uid)
		if !isOwner {
			return nil // молча: только владелец сервера
		}
		lang := bot.uiLang(c)
		chatID := m.Chat.ID
		chat, _ := bot.db.GetChat(chatID)
		if chat != nil && chat.OwnerID != uid {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "link_already_other"))
			return err
		}
		_, _ = bot.db.AddChat(chatID, uid, m.Chat.Title)
		_ = bot.db.LogAction(uid, "chat_link", fmt.Sprintf("chat %d", chatID), 0, chatID, 0)
		log.Printf("chat %d: owner %d linked chat (servers later)", chatID, uid)
		_, err := bot.b.Send(m.Chat, i18n.T(lang, "link_ok"))
		return err
	})
}

func (bot *Bot) cmdUnlink(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isGroup(c) {
			return nil
		}
		uid := m.Sender.ID
		chatID := m.Chat.ID
		lang := bot.uiLang(c)
		isChatOwner, _ := bot.db.IsChatOwner(chatID, uid)
		if !isChatOwner {
			return nil
		}
		chat, _ := bot.db.GetChat(chatID)
		if chat == nil {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "chat_not_linked_reply"))
			return err
		}
		if chat.OwnerID != uid {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "unlink_owner_only"))
			return err
		}
		if chat.PinnedMsgID.Valid && chat.PinnedMsgID.Int64 > 0 {
			_ = bot.b.Unpin(tele.ChatID(chatID), int(chat.PinnedMsgID.Int64))
		}
		_ = bot.db.RemoveChat(chatID)
		_ = bot.db.LogAction(uid, "chat_unlink", fmt.Sprintf("chat %d", chatID), 0, chatID, 0)
		_, err := bot.b.Send(m.Chat, i18n.T(lang, "unlink_ok"))
		return err
	})
}

func (bot *Bot) cmdStatus(c tele.Context) error {
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
		updateDash := false

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
			updateDash = true
		} else {
			return nil
		}

		if len(servers) == 0 {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_servers"))
			return err
		}
		if updateDash {
			bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		merged := make([]dashboard.DashServer, 0, len(servers))
		for _, s := range servers {
			merged = append(merged, bot.dash.GetAuthoritativeStatus(ctx, ownerID, s))
		}
		_, err := bot.b.Send(m.Chat, bot.dash.FormatDashboardText(merged, lang))
		return err
	})
}

func (bot *Bot) cmdRun(c tele.Context) error {
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
		chatIDs := []int64{}

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "need_onboarding"))
				return err
			}
			if bot.lockdownActive(uid) {
				_, _ = bot.b.Send(m.Chat, i18n.T(lang, "lockdown_blocked"))
				return nil
			}
			ownerID = uid
			servers, _ = bot.db.GetActiveServersByOwner(uid)
		} else if bot.isGroup(c) {
			owner, err := bot.db.GetChatOwner(chatID)
			if err != nil || owner == nil {
				return nil
			}
			if bot.lockdownActive(owner.UserID) {
				_, _ = bot.b.Send(m.Chat, i18n.T(lang, "lockdown_blocked"))
				return nil
			}
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_access_chat"))
				return err
			}
			ownerID = owner.UserID
			servers, _ = bot.db.GetChatServers(chatID)
			chatIDs = append(chatIDs, chatID)
		} else {
			return nil
		}

		if len(servers) == 0 {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_servers"))
			return err
		}

		// В группе с несколькими серверами — даём выбор, какой запустить.
		if bot.isGroup(c) && len(servers) > 1 {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "run_pick"), runServerPickerKB(servers, chatID, lang))
			return err
		}

		manager := bot.managers.For(ownerID)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		var started, skipped []string
		for _, s := range servers {
			text, err := manager.StartServer(ctx, s.ID)
			if err != nil {
				skipped = append(skipped, s.DisplayName+": "+friendlyStartError(err, lang))
				continue
			}
			started = append(started, s.DisplayName)
			_ = text
			bot.watcher.Start(bot.b, ownerID, s.ID)
		}
		if len(started) > 0 {
			bot.dash.MarkAsStarting(300)
			if len(chatIDs) == 0 {
				chats, _ := bot.db.GetChatsByOwner(ownerID)
				for _, ch := range chats {
					chatIDs = append(chatIDs, ch.ChatID)
				}
			}
			bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)
		}

		var lines []string
		if len(started) > 0 {
			lines = append(lines, i18n.T(lang, "run_started", strings.Join(started, ", ")))
		}
		if len(skipped) > 0 {
			lines = append(lines, i18n.T(lang, "run_skipped", strings.Join(skipped, "; ")))
		}
		if len(lines) == 0 {
			lines = append(lines, i18n.T(lang, "run_none"))
		}
		_, err := bot.b.Send(m.Chat, strings.Join(lines, "\n"))
		return err
	})
}

// cmdPlayers — список игроков онлайн (ЛС / группа, в группе — автоудаление).
func (bot *Bot) cmdPlayers(c tele.Context) error {
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

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var lines []string
		for _, s := range servers {
			st := bot.dash.GetAuthoritativeStatus(ctx, ownerID, s)
			if !st.IsOnline || len(st.PlayerList) == 0 {
				continue
			}
			name := s.DisplayName
			if name == "" {
				name = s.ServerIP
			}
			lines = append(lines,
				"🖥 <b>"+html.EscapeString(name)+"</b>",
				i18n.T(lang, "players_title", st.PlayersOnline, st.PlayersMax))
			for _, p := range st.PlayerList {
				lines = append(lines, "• "+html.EscapeString(p))
			}
			lines = append(lines, "")
		}
		if len(lines) == 0 {
			msg, err := bot.b.Send(m.Chat, i18n.T(lang, "players_none"))
			bot.autoDeleteInGroup(c, msg, 30)
			return err
		}
		text := strings.TrimSpace(strings.Join(lines, "\n"))
		msg, err := bot.b.Send(m.Chat, text)
		bot.autoDeleteInGroup(c, msg, 30)
		return err
	})
}

// cmdGrant / cmdRevoke — выдача/отзыв доступа в группе (только владелец чата).
func (bot *Bot) cmdGrant(c tele.Context) error {
	return bot.SafeCall(func() error {
		return bot.setAccessCmd(c, true)
	})
}

func (bot *Bot) cmdRevoke(c tele.Context) error {
	return bot.SafeCall(func() error {
		return bot.setAccessCmd(c, false)
	})
}

func (bot *Bot) setAccessCmd(c tele.Context, grant bool) error {
	m := c.Message()
	if m == nil || m.Sender == nil || !bot.isGroup(c) {
		return nil
	}
	uid := m.Sender.ID
	chatID := m.Chat.ID
	lang := bot.uiLang(c)
	isChatOwner, _ := bot.db.IsChatOwner(chatID, uid)
	if !isChatOwner {
		return nil // молча: только владелец чата
	}

	verb := "/grant"
	if !grant {
		verb = "/revoke"
	}
	usage := i18n.T(lang, "access_usage", verb, verb)

	parts := strings.Fields(m.Text)
	if len(parts) < 2 {
		_, err := bot.b.Send(m.Chat, usage)
		return err
	}
	raw := strings.TrimPrefix(parts[1], "@")
	if raw == "" {
		_, err := bot.b.Send(m.Chat, usage)
		return err
	}

	var targetID int64
	byUsername := strings.HasPrefix(parts[1], "@")
	if !byUsername {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			_, err := bot.b.Send(m.Chat, usage)
			return err
		}
		targetID = n
	} else {
		id, err := bot.db.GetUserIDByUsername(chatID, raw)
		if err != nil {
			return nil
		}
		if id == 0 {
			_, err := bot.b.Send(m.Chat,
				i18n.T(lang, "user_not_found", html.EscapeString(raw), verb))
			return err
		}
		targetID = id
	}

	display := raw
	if byUsername {
		display = "@" + raw
	}
	if grant {
		_ = bot.db.SetUserAccess(targetID, chatID, true)
		_ = bot.db.LogAction(uid, "access_grant",
			fmt.Sprintf("user %d (@%s)", targetID, raw), targetID, chatID, 0)
		_, err := bot.b.Send(m.Chat, i18n.T(lang, "access_granted", html.EscapeString(display)))
		return err
	}
	_ = bot.db.SetUserAccess(targetID, chatID, false)
	_ = bot.db.LogAction(uid, "access_revoke",
		fmt.Sprintf("user %d (@%s)", targetID, raw), targetID, chatID, 0)
	_, err := bot.b.Send(m.Chat, i18n.T(lang, "access_revoked", html.EscapeString(display)))
	return err
}

// cmdConfirm — ручное подтверждение очереди запуска Aternos (ЛС / группа).
func (bot *Bot) cmdConfirm(c tele.Context) error {
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
		chatIDs := []int64{}

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "need_onboarding"))
				return err
			}
			if bot.lockdownActive(uid) {
				_, _ = bot.b.Send(m.Chat, i18n.T(lang, "lockdown_blocked"))
				return nil
			}
			ownerID = uid
			servers, _ = bot.db.GetActiveServersByOwner(uid)
		} else if bot.isGroup(c) {
			owner, err := bot.db.GetChatOwner(chatID)
			if err != nil || owner == nil {
				return nil
			}
			if bot.lockdownActive(owner.UserID) {
				_, _ = bot.b.Send(m.Chat, i18n.T(lang, "lockdown_blocked"))
				return nil
			}
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_access_chat"))
				return err
			}
			ownerID = owner.UserID
			servers, _ = bot.db.GetChatServers(chatID)
			chatIDs = append(chatIDs, chatID)
		} else {
			return nil
		}

		if len(servers) == 0 {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "no_servers"))
			return err
		}

		manager := bot.managers.For(ownerID)
		var confirmed, skipped []string
		for _, s := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			err := manager.ConfirmServer(ctx, s.ID)
			cancel()
			if err != nil {
				skipped = append(skipped, s.DisplayName)
				continue
			}
			confirmed = append(confirmed, s.DisplayName)
			bot.dash.ClearQueuePosition(s.ID)
		}
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)

		var lines []string
		if len(confirmed) > 0 {
			lines = append(lines, i18n.T(lang, "confirm_ok_list", strings.Join(confirmed, ", ")))
		}
		if len(skipped) > 0 {
			lines = append(lines, i18n.T(lang, "confirm_skipped_list", strings.Join(skipped, ", ")))
		}
		if len(lines) == 0 {
			lines = append(lines, i18n.T(lang, "confirm_none"))
		}
		_, err := bot.b.Send(m.Chat, strings.Join(lines, "\n"))
		return err
	})
}

// cbRunServer — выбор сервера для /run: запускает конкретный сервер в чате.
func (bot *Bot) cbRunServer(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	serverID := cbInt(parts, 1)
	chatID := cbInt(parts, 2)
	uid := cb.Sender.ID
	lang := bot.uiLang(c)

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, i18n.T(lang, "chat_not_linked"), false)
		return nil
	}
	if bot.lockdownBlocked(c, owner.UserID) {
		return nil
	}
	if !bot.canManage(uid, chatID) {
		bot.answer(c, i18n.T(lang, "no_access_chat"), false)
		return nil
	}
	server, _ := bot.db.GetServer(serverID)
	if server == nil || server.OwnerID != owner.UserID {
		bot.answer(c, i18n.T(lang, "server_not_found"), false)
		return nil
	}

	manager := bot.managers.For(owner.UserID)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	text, err := manager.StartServer(ctx, serverID)
	if err != nil {
		bot.answer(c, friendlyStartError(err, lang), true)
		return nil
	}
	bot.dash.MarkAsStarting(300)
	bot.watcher.Start(bot.b, owner.UserID, serverID)
	bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	bot.answer(c, text, false)
	return nil
}

func (bot *Bot) cmdGroupHelp(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isGroup(c) {
			return nil
		}
		uid := m.Sender.ID
		chatID := m.Chat.ID
		lang := bot.uiLang(c)
		owner, _ := bot.db.GetChatOwner(chatID)
		if owner != nil && owner.UserID == uid {
			_, err := bot.b.Send(m.Chat, i18n.T(lang, "help_group_owner"))
			return err
		}
		_, err := bot.b.Send(m.Chat, i18n.T(lang, "help_group_user"))
		return err
	})
}

// canManage — владелец чата или участник с доступом (без учёта локдауна).
func (bot *Bot) canManage(uid, chatID int64) bool {
	isChatOwner, _ := bot.db.IsChatOwner(chatID, uid)
	if isChatOwner {
		return true
	}
	owner, _ := bot.db.GetChatOwner(chatID)
	if owner == nil || owner.LockdownMode {
		return false
	}
	ok, _ := bot.db.UserCanManage(uid, chatID)
	return ok
}

// toast — короткое всплывающее сообщение, которое само удаляется.
func (bot *Bot) toast(c tele.Context, text string, seconds int) {
	m := c.Message()
	if m == nil {
		return
	}
	msg, err := bot.b.Send(m.Chat, text)
	if err != nil {
		return
	}
	go func() {
		time.Sleep(time.Duration(seconds) * time.Second)
		_ = bot.b.Delete(msg)
	}()
}

// autoDeleteInGroup — автоудаление команды и её ответа в групповом чате
// (в ЛС сообщения остаются).
func (bot *Bot) autoDeleteInGroup(c tele.Context, msg *tele.Message, seconds int) {
	if !bot.isGroup(c) || msg == nil {
		return
	}
	m := c.Message()
	go func() {
		time.Sleep(time.Duration(seconds) * time.Second)
		if m != nil {
			_ = bot.b.Delete(m)
		}
		_ = bot.b.Delete(msg)
	}()
}

// ------------------------------------------------------------------ //
// кнопки дашборда: старт/стоп/подтверждение
// ------------------------------------------------------------------ //

// requireChatPermission — права на управление сервером в чате кнопки.
// Возвращает ownerID и true, если разрешено.
func (bot *Bot) requireChatPermission(c tele.Context) (int64, bool) {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Message.Chat == nil || cb.Sender == nil {
		bot.answer(c, "", false)
		return 0, false
	}
	chatID := cb.Message.Chat.ID
	lang := bot.uiLang(c)
	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, i18n.T(lang, "chat_not_linked"), false)
		return 0, false
	}
	if owner.UserID == cb.Sender.ID {
		return owner.UserID, true
	}
	if owner.LockdownMode {
		bot.answer(c, i18n.T(lang, "lockdown_blocked"), false)
		return 0, false
	}
	ok, _ := bot.db.UserCanManage(cb.Sender.ID, chatID)
	if !ok {
		bot.answer(c, i18n.T(lang, "no_access_hint"), false)
		return 0, false
	}
	return owner.UserID, true
}

func (bot *Bot) cbServerAction(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	lang := bot.uiLang(c)
	if cb.Message.Chat.Type == tele.ChatPrivate {
		bot.answer(c, i18n.T(lang, "btn_works_group"), false)
		return nil
	}
	ownerID, ok := bot.requireChatPermission(c)
	if !ok {
		return nil
	}
	chatID := cb.Message.Chat.ID
	serverID := cbInt(parts, 1)
	action := cbStr(parts, 2)

	manager := bot.managers.For(ownerID)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "start":
		if bot.lockdownBlocked(c, ownerID) {
			return nil
		}
		text, err := manager.StartServer(ctx, serverID)
		if err != nil {
			bot.answer(c, friendlyStartError(err, lang), true)
			return nil
		}
		bot.dash.MarkAsStarting(300)
		bot.watcher.Start(bot.b, ownerID, serverID)
		bot.answer(c, text, false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	case "stop":
		text, err := manager.StopServer(ctx, serverID)
		if err != nil {
			bot.answer(c, friendlyStartError(err, lang), true)
			return nil
		}
		bot.dash.ClearQueuePosition(serverID)
		bot.answer(c, text, false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	case "confirm":
		if bot.lockdownBlocked(c, ownerID) {
			return nil
		}
		if err := manager.ConfirmServer(ctx, serverID); err != nil {
			bot.answer(c, friendlyStartError(err, lang), true)
			return nil
		}
		bot.dash.ClearQueuePosition(serverID)
		bot.answer(c, i18n.T(lang, "confirm_ok"), false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	}
	return nil
}

func (bot *Bot) cbRefreshDashboard(c tele.Context) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Message.Chat == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	lang := bot.uiLang(c)
	chatID := cb.Message.Chat.ID
	if cb.Message.Chat.Type == tele.ChatPrivate {
		bot.answer(c, i18n.T(lang, "btn_works_group"), false)
		return nil
	}
	owner, _ := bot.db.GetChatOwner(chatID)
	if owner == nil {
		bot.answer(c, i18n.T(lang, "chat_not_linked"), false)
		return nil
	}
	if owner.UserID != cb.Sender.ID {
		ok, _ := bot.db.UserCanManage(cb.Sender.ID, chatID)
		if !ok {
			return nil // нет доступа — молча
		}
	}
	bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	bot.answer(c, i18n.T(lang, "dash_updated"), false)
	return nil
}

// ------------------------------------------------------------------ //
// запрос и одобрение доступа
// ------------------------------------------------------------------ //

func (bot *Bot) cbRequestAccess(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	chatID := cbInt(parts, 1)
	lang := bot.uiLang(c)

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, i18n.T(lang, "chat_not_linked"), false)
		return nil
	}
	if owner.UserID == uid {
		bot.answer(c, i18n.T(lang, "you_are_owner"), false)
		return nil
	}
	ok, _ := bot.db.UserCanManage(uid, chatID)
	if ok {
		bot.answer(c, i18n.T(lang, "already_has_access"), false)
		return nil
	}

	bot.mu.Lock()
	last := bot.lastAccessReq[uid]
	bot.mu.Unlock()
	if time.Since(last) < accessReqCooldown {
		bot.answer(c, i18n.T(lang, "access_cooldown"), false)
		return nil
	}
	bot.mu.Lock()
	bot.lastAccessReq[uid] = time.Now()
	bot.mu.Unlock()

	_ = bot.db.LogAction(owner.UserID, "access_request",
		fmt.Sprintf("user %d (@%s)", uid, cb.Sender.Username), uid, chatID, 0)
	chatTitle := ""
	if cb.Message.Chat != nil {
		chatTitle = cb.Message.Chat.Title
	}
	_, err = bot.b.Send(&tele.Chat{ID: owner.UserID},
		i18n.T(bot.ownerLang(owner.UserID), "access_req_title",
			chatTitle,
			chatID,
			html.EscapeString(cb.Sender.FirstName+" "+cb.Sender.LastName),
			html.EscapeString(cb.Sender.Username)),
		approveAccessKB(uid, chatID, bot.ownerLang(owner.UserID)))
	if err != nil {
		log.Printf("failed to deliver access request to owner %d: %v", owner.UserID, err)
		bot.answer(c, i18n.T(lang, "access_req_fail"), false)
		return nil
	}
	bot.answer(c, i18n.T(lang, "access_req_sent"), false)
	return nil
}

func (bot *Bot) cbApproveAccess(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	userID := cbInt(parts, 1)
	chatID := cbInt(parts, 2)
	approve := parseBool(cbStr(parts, 3))
	lang := bot.uiLang(c)

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil || owner.UserID != uid {
		bot.answer(c, i18n.T(lang, "access_only_owner"), false)
		return nil
	}

	if approve {
		_ = bot.db.SetUserAccess(userID, chatID, true)
		_ = bot.db.LogAction(owner.UserID, "access_grant",
			fmt.Sprintf("user %d", userID), userID, chatID, 0)
		_ = bot.edit(cb.Message, cb.Message.Text+i18n.T(lang, "access_approved_edit"), nil)
		_, _ = bot.b.Send(&tele.Chat{ID: userID},
			i18n.T(bot.userLang(userID), "access_approved_pm", cb.Message.Chat.Title))
		_, _ = bot.b.Send(&tele.Chat{ID: chatID}, i18n.T(lang, "access_approved_chat"))
	} else {
		_ = bot.db.LogAction(owner.UserID, "access_deny",
			fmt.Sprintf("user %d", userID), userID, chatID, 0)
		_ = bot.edit(cb.Message, cb.Message.Text+i18n.T(lang, "access_denied_edit"), nil)
	}
	return nil
}

// ------------------------------------------------------------------ //
// текст (FSM-маршрутизация)
// ------------------------------------------------------------------ //

func (bot *Bot) onText(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		uid := m.Sender.ID
		switch bot.fsm.Get(uid) {
		case fsmOnbWaitingCookie:
			bot.onSessionCookie(c)
		case fsmOnbSelecting:
			// выбор серверов — только кнопками; текст игнорируем
		case fsmAdminWaitCookie:
			bot.onNewCookieMessage(c)
		}
		return nil
	})
}

// ------------------------------------------------------------------ //
// дружелюбные ошибки запуска
// ------------------------------------------------------------------ //

// friendlyStartError — дружелюбное описание ошибки запуска: если сервер
// уже запускается или оффлайн, пользователю показывается нейтральное
// сообщение без сырых деталей и без каких-либо уведомлений Владельцу.
func friendlyStartError(err error, lang string) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "already"):
		return i18n.T(lang, "friendly_already")
	case strings.Contains(msg, "cloudflare"):
		return i18n.T(lang, "friendly_cloudflare")
	case strings.Contains(msg, "session"):
		return i18n.T(lang, "friendly_session")
	}
	return i18n.T(lang, "friendly_unavailable")
}