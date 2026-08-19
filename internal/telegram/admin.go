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
)

const usersPageSize = 10

// ------------------------------------------------------------------ //
// /start, /panel, /help (ЛС)
// ------------------------------------------------------------------ //

func (bot *Bot) cmdStart(c tele.Context) error {
	return bot.SafeCall(func() error {
		if !bot.isPrivate(c) {
			return nil
		}
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		uid := m.Sender.ID
		isOwner, err := bot.db.IsOwner(uid)
		if err != nil {
			return nil
		}
		if isOwner {
			_ = bot.db.UpdateOwnerProfile(uid, m.Sender.Username, m.Sender.FirstName+" "+m.Sender.LastName)
			_, err = bot.b.Send(m.Chat, bot.panelText(uid), ownerPanelKB())
			return err
		}
		bot.startOnboarding(c)
		return nil
	})
}

func (bot *Bot) cmdPanel(c tele.Context) error {
	return bot.SafeCall(func() error {
		if !bot.isPrivate(c) {
			return nil
		}
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		isOwner, _ := bot.db.IsOwner(m.Sender.ID)
		if !isOwner {
			_, err := bot.b.Send(m.Chat, "Сначала пройдите онбординг: отправьте куку ATERNOS_SESSION.")
			return err
		}
		_, err := bot.b.Send(m.Chat, bot.panelText(m.Sender.ID), ownerPanelKB())
		return err
	})
}

func (bot *Bot) cmdHelp(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(m.Sender.ID)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, "Напишите /start, чтобы пройти онбординг.")
				return err
			}
			_, err := bot.b.Send(m.Chat,
				"<b>Команды владельца:</b>\n"+
					"/panel — панель (серверы, чаты, настройки, аудит)\n"+
					"/run — запустить все серверы\n"+
					"/confirm — подтвердить очередь запуска\n"+
					"/status — статус всех серверов\n"+
					"/set_session — обновить куку Aternos\n"+
					"/emergency — экстренный локдаун (права всем: OFF)\n\n"+
					"<b>В группе:</b>\n"+
					"/link — привязать чат (серверы выбираются в ЛС)\n"+
					"/unlink — отвязать чат\n"+
					"/run — запустить серверы чата\n"+
					"/confirm — подтвердить очередь\n"+
					"/status — статус серверов чата")
			return err
		}
		return bot.cmdGroupHelp(c)
	})
}

func (bot *Bot) panelText(uid int64) string {
	owner, _ := bot.db.GetOwner(uid)
	servers, _ := bot.db.GetActiveServersByOwner(uid)
	chats, _ := bot.db.GetChatsByOwner(uid)
	lockdown := "выкл"
	if owner != nil && owner.LockdownMode {
		lockdown = "🔒 вкл"
	}
	fullName := fmt.Sprint(uid)
	if owner != nil && owner.FullName != "" {
		fullName = owner.FullName
	}
	return "🛠 <b>Панель владельца</b>\n\n" +
		"👤 " + html.EscapeString(fullName) + "\n" +
		fmt.Sprintf("🖥 Серверы: %d\n", len(servers)) +
		fmt.Sprintf("💬 Чаты: %d\n", len(chats)) +
		"🔒 Локдаун: " + lockdown + "\n\n" +
		"Выберите раздел:"
}

func (bot *Bot) isPrivate(c tele.Context) bool {
	return c.Message() != nil && c.Message().Chat != nil && c.Message().Chat.Type == tele.ChatPrivate
}

func (bot *Bot) requireOwner(c tele.Context, uid int64) bool {
	isOwner, err := bot.db.IsOwner(uid)
	if err != nil || !isOwner {
		bot.answer(c, "Нет доступа: вы не владелец.", false)
		return false
	}
	return true
}

// ------------------------------------------------------------------ //
// панель: навигация
// ------------------------------------------------------------------ //

func (bot *Bot) cbPanel(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	action := cbStr(parts, 1)
	switch action {
	case "servers":
		servers, _ := bot.db.GetActiveServersByOwner(uid)
		text := "<b>Ваши серверы:</b>\n"
		if len(servers) == 0 {
			text = "<b>Ваши серверы:</b>\nНет серверов. Пройдите онбординг заново: /start."
		}
		_ = bot.edit(cb.Message, text, ownerServersKB(servers))
	case "chats":
		chats, _ := bot.db.GetChatsByOwner(uid)
		text := "<b>Ваши чаты:</b>\n"
		if len(chats) == 0 {
			text = "<b>Ваши чаты:</b>\nНет чатов. Добавьте бота в группу и напишите /link."
		}
		_ = bot.edit(cb.Message, text, ownerChatsKB(chats))
	case "settings":
		owner, _ := bot.db.GetOwner(uid)
		servers, _ := bot.db.GetActiveServersByOwner(uid)
		autoConfirm := false
		if len(servers) > 0 {
			autoConfirm = servers[0].AutoConfirm
		}
		lockdown := false
		if owner != nil {
			lockdown = owner.LockdownMode
		}
		_ = bot.edit(cb.Message, "⚙️ <b>Настройки</b> (применяются ко всем серверам):",
			ownerSettingsKB(lockdown, autoConfirm))
	case "audit":
		logs, _ := bot.db.GetAuditLog(uid, 10)
		var text string
		if len(logs) == 0 {
			text = "📋 <b>Аудит:</b>\nПока нет записей."
		} else {
			lines := []string{"📋 <b>Аудит:</b>"}
			for _, e := range logs {
				details := e.Details
				if details == "" {
					details = "-"
				}
				lines = append(lines, "• "+html.EscapeString(e.Action)+" — "+html.EscapeString(details))
			}
			text = strings.Join(lines, "\n")
		}
		_ = bot.edit(cb.Message, text, ownerAuditKB())
	case "refresh_servers":
		bot.refreshServers(c)
	case "refresh_session":
		bot.fsm.Set(uid, fsmAdminWaitCookie)
		_ = bot.edit(cb.Message,
			"🔑 Отправь новую куку <code>ATERNOS_SESSION</code> — сообщение будет сразу удалено.", nil)
	case "delete_account":
		_ = bot.edit(cb.Message,
			"🗑 <b>Удалить аккаунт?</b>\n\n"+
				"Будут удалены все серверы и отвязаны все чаты. "+
				"Онбординг можно будет пройти заново через /start.",
			deleteAccountKB())
	case "back":
		_ = bot.edit(cb.Message, bot.panelText(uid), ownerPanelKB())
	}
	return nil
}

// refreshServers — «🔄 Обновить серверы»: сверка с аккаунтом Aternos.
func (bot *Bot) refreshServers(c tele.Context) {
	cb := c.Callback()
	uid := cb.Sender.ID
	manager := bot.managers.For(uid)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	aternosServers, err := manager.ListAccountServers(ctx)
	if err != nil {
		_ = bot.edit(cb.Message, "❌ "+err.Error(), ownerPanelKB())
		return
	}
	existing, _ := bot.db.GetServersByOwner(uid)
	byID := map[string]*database.Server{}
	for _, s := range existing {
		byID[s.AternosID] = s
	}
	fetched := map[string]bool{}
	for _, s := range aternosServers {
		fetched[s.AternosID] = true
	}

	var added, reactivated, removed []string
	touchedChats := map[int64]bool{}
	for _, s := range aternosServers {
		if old, ok := byID[s.AternosID]; ok {
			if !old.IsActive {
				_ = bot.db.SetServerActive(old.ID, true)
				reactivated = append(reactivated, s.DisplayName)
			}
		} else {
			id, err := bot.db.AddServer(uid, s.AternosID, s.ServerIP, s.DisplayName)
			if err == nil && id > 0 {
				added = append(added, s.DisplayName)
			}
		}
	}
	for _, row := range existing {
		if !fetched[row.AternosID] && row.IsActive {
			chats, _ := bot.db.GetChatsForServer(row.ID)
			for _, ch := range chats {
				touchedChats[ch.ChatID] = true
			}
			_ = bot.db.UnbindServerFromAllChats(row.ID)
			_ = bot.db.DeactivateServer(row.ID)
			removed = append(removed, row.DisplayName)
		}
	}
	_ = bot.db.LogAction(uid, "servers_refresh",
		"+ "+strings.Join(added, ", ")+" ~ "+strings.Join(reactivated, ", ")+" - "+strings.Join(removed, ", "),
		0, 0, 0)
	_ = bot.edit(cb.Message, bot.panelText(uid), ownerPanelKB())
	if len(touchedChats) > 0 {
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, dashboard.SortedChatIDs(keysOf(touchedChats)))
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "➕ Добавлены: "+strings.Join(added, ", "))
	}
	if len(reactivated) > 0 {
		parts = append(parts, "↩️ Возвращены: "+strings.Join(reactivated, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "➖ Отключены: "+strings.Join(removed, ", "))
	}
	if len(parts) > 0 {
		text := strings.Join(parts, "\n")
		if len(text) > 200 {
			text = text[:200]
		}
		bot.answer(c, text, false)
	}
}

func keysOf(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ------------------------------------------------------------------ //
// панель: серверы
// ------------------------------------------------------------------ //

func (bot *Bot) cbPanelServer(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	server, _ := bot.db.GetServer(cbInt(parts, 1))
	if server == nil || server.OwnerID != uid {
		bot.answer(c, "Сервер не найден.", false)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	status := bot.dash.GetAuthoritativeStatus(ctx, uid, server)
	stateText := "🔴 ОФФЛАЙН"
	playersText := ""
	if status.IsOnline {
		stateText = "🟢 ОНЛАЙН"
		playersText = fmt.Sprintf(" · %d/%d игроков", status.PlayersOnline, status.PlayersMax)
	}
	autoConfirm := "выкл"
	if server.AutoConfirm {
		autoConfirm = "вкл"
	}
	ip := server.ServerIP
	if ip == "" {
		ip = "—"
	}
	_ = bot.edit(cb.Message,
		"🖥 <b>"+html.EscapeString(server.DisplayName)+"</b>\n"+
			"🌐 IP: "+html.EscapeString(ip)+"\n"+
			fmt.Sprintf("%s%s\n", stateText, playersText)+
			"✅ Автоподтверждение: "+autoConfirm+"\n\n"+
			"<i>Настройка автоподтверждения — в разделе «Настройки».</i>",
		serverCardKB(server.ID, status.IsOnline))
	return nil
}

func (bot *Bot) cbPanelAction(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	server, _ := bot.db.GetServer(cbInt(parts, 1))
	if server == nil || server.OwnerID != uid {
		bot.answer(c, "Сервер не найден.", true)
		return nil
	}
	action := cbStr(parts, 2)
	manager := bot.managers.For(uid)
	chats, _ := bot.db.GetChatsByOwner(uid)
	chatIDs := make([]int64, 0, len(chats))
	for _, ch := range chats {
		chatIDs = append(chatIDs, ch.ChatID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "start":
		if bot.lockdownBlocked(c, uid) {
			return nil
		}
		text, err := manager.StartServer(ctx, server.ID)
		if err != nil {
			bot.answer(c, err.Error(), true)
			return nil
		}
		bot.dash.MarkAsStarting(300)
		bot.watcher.Start(bot.b, uid, server.ID)
		bot.answer(c, text, false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)
	case "stop":
		text, err := manager.StopServer(ctx, server.ID)
		if err != nil {
			bot.answer(c, err.Error(), true)
			return nil
		}
		bot.dash.ClearQueuePosition(server.ID)
		bot.answer(c, text, false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)
	case "confirm":
		if bot.lockdownBlocked(c, uid) {
			return nil
		}
		if err := manager.ConfirmServer(ctx, server.ID); err != nil {
			bot.answer(c, err.Error(), true)
			return nil
		}
		bot.dash.ClearQueuePosition(server.ID)
		bot.answer(c, "✅ Запуск подтверждён.", false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, chatIDs)
	case "delete":
		serverChats, _ := bot.db.GetChatsForServer(server.ID)
		serverChatIDs := make([]int64, 0, len(serverChats))
		for _, ch := range serverChats {
			serverChatIDs = append(serverChatIDs, ch.ChatID)
		}
		_ = bot.db.UnbindServerFromAllChats(server.ID)
		_ = bot.db.DeactivateServer(server.ID)
		_ = bot.db.LogAction(uid, "server_deactivate", server.DisplayName, 0, 0, server.ID)
		bot.answer(c, "Сервер отключён.", false)
		servers, _ := bot.db.GetActiveServersByOwner(uid)
		_ = bot.edit(cb.Message, "<b>Ваши серверы:</b>", ownerServersKB(servers))
		if len(serverChatIDs) > 0 {
			bot.dash.UpdateChatsDashboards(context.Background(), bot.b, serverChatIDs)
		}
	}
	return nil
}

// ------------------------------------------------------------------ //
// панель: чаты и участники
// ------------------------------------------------------------------ //

func (bot *Bot) cbPanelChat(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	chatID := cbInt(parts, 1)
	action := cbStr(parts, 2)
	chat, _ := bot.db.GetChat(chatID)
	if chat == nil || chat.OwnerID != uid {
		bot.answer(c, "Чат не найден или не принадлежит вам.", false)
		return nil
	}

	switch action {
	case "select":
		linked, _ := bot.db.GetChatServers(chatID)
		_ = bot.edit(cb.Message, chatCardText(chat, linked),
			chatCardKB(chatID, bot.ownerServers(uid), linkedIDsOf(linked)))
	case "unlink":
		pinned := chat.PinnedMsgID
		if pinned.Valid && pinned.Int64 > 0 {
			_ = bot.b.Unpin(tele.ChatID(chatID), int(pinned.Int64))
		}
		_ = bot.db.RemoveChat(chatID)
		_ = bot.db.LogAction(uid, "chat_unlink", fmt.Sprintf("chat %d", chatID), 0, chatID, 0)
		bot.answer(c, "Чат отвязан.", false)
		chats, _ := bot.db.GetChatsByOwner(uid)
		_ = bot.edit(cb.Message, "<b>Ваши чаты:</b>", ownerChatsKB(chats))
	case "users":
		bot.showUsersPage(c, uid, chatID, 0)
	}
	return nil
}

func (bot *Bot) cbUsersPage(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	chatID := cbInt(parts, 1)
	page := int(cbInt(parts, 2))
	if page < 0 {
		page = 0
	}
	chat, _ := bot.db.GetChat(chatID)
	if chat == nil || chat.OwnerID != uid {
		bot.answer(c, "Чат не найден или не принадлежит вам.", false)
		return nil
	}
	bot.showUsersPage(c, uid, chatID, page)
	return nil
}

func (bot *Bot) showUsersPage(c tele.Context, uid, chatID int64, page int) {
	cb := c.Callback()
	total, _ := bot.db.GetChatUsersCount(chatID)
	users, _ := bot.db.GetChatUsersPaginated(chatID, usersPageSize, page*usersPageSize)
	lines := []string{fmt.Sprintf("👥 <b>Участники чата</b> (всего: %d)", total)}
	for _, u := range users {
		access := "—"
		if u.HasAccess {
			access = "✅"
		}
		name := u.FullName
		if name == "" {
			if u.Username != "" {
				name = "@" + u.Username
			} else {
				name = fmt.Sprintf("id%d", u.UserID)
			}
		}
		lines = append(lines, access+" "+html.EscapeString(name))
	}
	if len(users) == 0 {
		lines = append(lines, "Пока никто не зарегистрирован.")
	}
	_ = bot.edit(cb.Message, strings.Join(lines, "\n"), usersPageKB(chatID, page, total, usersPageSize))
}

func (bot *Bot) ownerServers(uid int64) []*database.Server {
	servers, _ := bot.db.GetActiveServersByOwner(uid)
	return servers
}

func linkedIDsOf(servers []*database.Server) map[int64]bool {
	out := map[int64]bool{}
	for _, s := range servers {
		out[s.ID] = true
	}
	return out
}

func chatCardText(chat *database.Chat, linked []*database.Server) string {
	names := make([]string, 0, len(linked))
	for _, s := range linked {
		names = append(names, s.DisplayName)
	}
	linkedText := "нет"
	if len(names) > 0 {
		linkedText = strings.Join(names, ", ")
	}
	title := chat.Title
	if title == "" {
		title = strconv.FormatInt(chat.ChatID, 10)
	}
	return "💬 <b>" + html.EscapeString(title) + "</b>\n" +
		"ID: <code>" + strconv.FormatInt(chat.ChatID, 10) + "</code>\n" +
		"🔗 Серверы в группе: " + html.EscapeString(linkedText) + "\n\n" +
		"Нажмите на сервер, чтобы подключить или отключить его в этой группе:"
}

func (bot *Bot) cbPanelChatServer(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	chatID := cbInt(parts, 1)
	serverID := cbInt(parts, 2)
	action := cbStr(parts, 3)
	chat, _ := bot.db.GetChat(chatID)
	if chat == nil || chat.OwnerID != uid {
		bot.answer(c, "Чат не найден или не принадлежит вам.", false)
		return nil
	}
	server, _ := bot.db.GetServer(serverID)
	if server == nil || server.OwnerID != uid {
		bot.answer(c, "Сервер не найден.", false)
		return nil
	}

	if action == "bind" {
		_ = bot.db.LinkServerToChat(chatID, serverID)
		_ = bot.db.LogAction(uid, "chat_server_bind",
			server.DisplayName+" -> chat "+strconv.FormatInt(chatID, 10), 0, chatID, serverID)
		bot.answer(c, "✅ "+server.DisplayName+" подключён к группе.", false)
	} else {
		_ = bot.db.UnlinkServerFromChat(chatID, serverID)
		_ = bot.db.LogAction(uid, "chat_server_unbind",
			server.DisplayName+" -> chat "+strconv.FormatInt(chatID, 10), 0, chatID, serverID)
		bot.answer(c, "🚫 "+server.DisplayName+" отключён от группы.", false)
	}
	bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})

	linked, _ := bot.db.GetChatServers(chatID)
	_ = bot.edit(cb.Message, chatCardText(chat, linked),
		chatCardKB(chatID, bot.ownerServers(uid), linkedIDsOf(linked)))
	return nil
}

// ------------------------------------------------------------------ //
// панель: настройки
// ------------------------------------------------------------------ //

func (bot *Bot) cbOwnerSettings(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	action := cbStr(parts, 1)
	enabled := parseBool(cbStr(parts, 2))

	if action == "lockdown" {
		_ = bot.db.SetOwnerLockdown(uid, enabled)
		_ = bot.db.LogAction(uid, "lockdown", boolText(enabled), 0, 0, 0)
		chats, _ := bot.db.GetChatsByOwner(uid)
		chatIDs := make([]int64, 0, len(chats))
		for _, ch := range chats {
			chatIDs = append(chatIDs, ch.ChatID)
		}
		stateText := "✅ Локдаун снят."
		if enabled {
			stateText = "🔒 Локдаун включён: управление серверами приостановлено."
		}
		bot.dash.BroadcastMessage(bot.b, chatIDs, stateText)
		bot.answer(c, stateText, false)
	} else if action == "auto_confirm" {
		servers, _ := bot.db.GetActiveServersByOwner(uid)
		for _, s := range servers {
			_ = bot.db.SetServerAutoConfirm(s.ID, enabled)
		}
		_ = bot.db.LogAction(uid, "auto_confirm", "все серверы: "+boolText(enabled), 0, 0, 0)
		bot.answer(c, "Автоподтверждение: "+boolText(enabled)+" (все серверы)", false)
	}
	bot.refreshSettings(cb, uid)
	return nil
}

func (bot *Bot) refreshSettings(cb *tele.Callback, uid int64) {
	owner, _ := bot.db.GetOwner(uid)
	servers, _ := bot.db.GetActiveServersByOwner(uid)
	autoConfirm := false
	if len(servers) > 0 {
		autoConfirm = servers[0].AutoConfirm
	}
	lockdown := false
	if owner != nil {
		lockdown = owner.LockdownMode
	}
	_ = bot.edit(cb.Message, "⚙️ <b>Настройки</b> (применяются ко всем серверам):",
		ownerSettingsKB(lockdown, autoConfirm))
}

func boolText(b bool) string {
	if b {
		return "вкл"
	}
	return "выкл"
}

// ------------------------------------------------------------------ //
// /set_session, /emergency, /announce
// ------------------------------------------------------------------ //

func (bot *Bot) cmdSetSession(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil || !bot.isPrivate(c) {
			return nil
		}
		uid := m.Sender.ID
		isOwner, _ := bot.db.IsOwner(uid)
		if !isOwner {
			_, err := bot.b.Send(m.Chat, "❌ Сначала пройдите онбординг (/start).")
			return err
		}
		parts := strings.SplitN(m.Text, " ", 2)
		cookie := ""
		if len(parts) > 1 {
			cookie = decodeCookie(parts[1])
		}
		if cookie == "" {
			_, err := bot.b.Send(m.Chat, "Использование: /set_session <кука ATERNOS_SESSION>")
			return err
		}
		_ = bot.b.Delete(m)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := bot.managers.For(uid).UpdateSession(ctx, cookie); err != nil {
			_, e := bot.b.Send(m.Chat, "❌ "+err.Error())
			return e
		}
		_, err := bot.b.Send(m.Chat, "✅ Кука обновлена! Кеш клиента сброшен.")
		return err
	})
}

func (bot *Bot) cmdEmergency(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		uid := m.Sender.ID
		if uid != bot.cfg.SuperAdminID {
			return nil // молча: только суперадмин
		}
		owner, _ := bot.db.GetOwner(uid)
		if owner == nil {
			_, err := bot.b.Send(m.Chat, "Сначала пройдите онбординг: /start.")
			return err
		}
		// 1) Мгновенно отбираем права у ВСЕХ пользователей всех чатов.
		if err := bot.db.RevokeAllAccess(); err != nil {
			log.Printf("emergency: сброс прав не удался: %v", err)
		}
		// 2) Глобальный флаг lockdown_mode = ON.
		_ = bot.db.SetOwnerLockdown(uid, true)
		_ = bot.db.LogAction(uid, "emergency",
			"полный локдаун: права всех пользователей отозваны, запуск невозможен", 0, 0, 0)
		chats, _ := bot.db.GetChatsByOwner(uid)
		chatIDs := make([]int64, 0, len(chats))
		for _, ch := range chats {
			chatIDs = append(chatIDs, ch.ChatID)
		}
		text := "🚨 <b>Экстренная блокировка включена.</b>\n" +
			"Права всех пользователей отозваны (has_access=0). Запуск серверов невозможен, " +
			"пока локдаун не будет снят в /panel → ⚙️ Настройки → «Локдаун»."
		bot.dash.BroadcastMessage(bot.b, chatIDs, text)
		_, err := bot.b.Send(m.Chat, text)
		return err
	})
}

func (bot *Bot) cmdAnnounce(c tele.Context) error {
	return bot.SafeCall(func() error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		if m.Sender.ID != bot.cfg.SuperAdminID {
			return nil
		}
		parts := strings.SplitN(m.Text, " ", 2)
		text := ""
		if len(parts) > 1 {
			text = strings.TrimSpace(parts[1])
		}
		if text == "" {
			_, err := bot.b.Send(m.Chat, "Использование: /announce <текст>")
			return err
		}
		owners, _ := bot.db.GetAllOwners()
		sent := 0
		for _, owner := range owners {
			if _, err := bot.b.Send(&tele.Chat{ID: owner.UserID}, "📢 "+text); err == nil {
				sent++
			} else {
				log.Printf("announce: владельцу %d не доставлено: %v", owner.UserID, err)
			}
		}
		_, err := bot.b.Send(m.Chat,
			fmt.Sprintf("Рассылка отправлена %d/%d владельцам.", sent, len(owners)))
		return err
	})
}

// ------------------------------------------------------------------ //
// удаление аккаунта
// ------------------------------------------------------------------ //

func (bot *Bot) cbDeleteAccount(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	uid := cb.Sender.ID
	if !bot.requireOwner(c, uid) {
		return nil
	}
	if !parseBool(cbStr(parts, 1)) {
		_ = bot.edit(cb.Message, bot.panelText(uid), ownerPanelKB())
		return nil
	}
	chats, _ := bot.db.GetChatsByOwner(uid)
	pmPinned, _ := bot.db.GetOwnerPmPinned(uid)
	_ = bot.db.DeleteOwner(uid)
	for _, ch := range chats {
		if ch.PinnedMsgID.Valid && ch.PinnedMsgID.Int64 > 0 {
			_ = bot.b.Unpin(tele.ChatID(ch.ChatID), int(ch.PinnedMsgID.Int64))
		}
	}
	if pmPinned > 0 {
		_ = bot.b.Unpin(tele.ChatID(uid), int(pmPinned))
	}
	_ = bot.edit(cb.Message, "🗑 <b>Аккаунт удалён.</b>\nНапишите /start, чтобы пройти онбординг заново.", nil)
	return nil
}

// ------------------------------------------------------------------ //
// обновление куки из панели (FSM admin:waiting_cookie)
// ------------------------------------------------------------------ //

func (bot *Bot) onNewCookieMessage(c tele.Context) {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return
	}
	cookie := decodeCookie(m.Text)
	_ = bot.b.Delete(m)
	uid := m.Sender.ID
	bot.fsm.Clear(uid)
	if cookie == "" {
		return
	}
	manager := bot.managers.For(uid)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := manager.ProbeCookie(ctx, cookie); err != nil {
		_, _ = bot.b.Send(m.Chat, "❌ Кука недействительна: "+err.Error())
		return
	}
	if err := manager.UpdateSession(ctx, cookie); err != nil {
		_, _ = bot.b.Send(m.Chat, "❌ "+err.Error())
		return
	}
	_, _ = bot.b.Send(m.Chat, "✅ Кука обновлена! Кеш клиента сброшен.")
}