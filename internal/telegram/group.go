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
		chatID := m.Chat.ID
		chat, _ := bot.db.GetChat(chatID)
		if chat != nil && chat.OwnerID != uid {
			_, err := bot.b.Send(m.Chat, "❌ Этот чат уже привязан к другому владельцу.")
			return err
		}
		_, _ = bot.db.AddChat(chatID, uid, m.Chat.Title)
		_ = bot.db.LogAction(uid, "chat_link", fmt.Sprintf("chat %d", chatID), 0, chatID, 0)
		log.Printf("чат %d: владелец %d привязал чат (серверы — позже)", chatID, uid)
		_, err := bot.b.Send(m.Chat,
			"✅ Чат привязан!\n\n"+
				"Теперь выберите, какие серверы будут доступны в этой группе:\n"+
				"📱 ЛС с ботом → /panel → 💬 Чаты → «🔗 Серверы чата».\n\n"+
				"Дашборд появится, как только вы подключите первый сервер.")
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
		isChatOwner, _ := bot.db.IsChatOwner(chatID, uid)
		if !isChatOwner {
			return nil
		}
		chat, _ := bot.db.GetChat(chatID)
		if chat == nil {
			_, err := bot.b.Send(m.Chat, "Чат не привязан.")
			return err
		}
		if chat.OwnerID != uid {
			_, err := bot.b.Send(m.Chat, "❌ Только владелец чата может отвязать его.")
			return err
		}
		if chat.PinnedMsgID.Valid && chat.PinnedMsgID.Int64 > 0 {
			_ = bot.b.Unpin(tele.ChatID(chatID), int(chat.PinnedMsgID.Int64))
		}
		_ = bot.db.RemoveChat(chatID)
		_ = bot.db.LogAction(uid, "chat_unlink", fmt.Sprintf("chat %d", chatID), 0, chatID, 0)
		_, err := bot.b.Send(m.Chat, "✅ Чат отвязан. Дашборд остановлен.")
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

		var ownerID int64
		var servers []*database.Server
		updateDash := false

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, "Сначала пройдите онбординг: /start.")
				return err
			}
			ownerID = uid
			servers, _ = bot.db.GetActiveServersByOwner(uid)
		} else if bot.isGroup(c) {
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, "У вас нет доступа к серверам этого чата.")
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
			_, err := bot.b.Send(m.Chat, "Нет подключённых серверов.")
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
		_, err := bot.b.Send(m.Chat, bot.dash.FormatDashboardText(merged))
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

		var ownerID int64
		var servers []*database.Server
		chatIDs := []int64{}

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, "Сначала пройдите онбординг: /start.")
				return err
			}
			if bot.lockdownActive(uid) {
				_, _ = bot.b.Send(m.Chat, msgLockdownBlocked)
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
				_, _ = bot.b.Send(m.Chat, msgLockdownBlocked)
				return nil
			}
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, "У вас нет доступа к серверам этого чата.")
				return err
			}
			ownerID = owner.UserID
			servers, _ = bot.db.GetChatServers(chatID)
			chatIDs = append(chatIDs, chatID)
		} else {
			return nil
		}

		if len(servers) == 0 {
			_, err := bot.b.Send(m.Chat, "Нет подключённых серверов.")
			return err
		}

		// В группе с несколькими серверами — даём выбор, какой запустить.
		if bot.isGroup(c) && len(servers) > 1 {
			_, err := bot.b.Send(m.Chat, "▶️ <b>Какой сервер запустить?</b>", runServerPickerKB(servers, chatID))
			return err
		}

		manager := bot.managers.For(ownerID)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		var started, skipped []string
		for _, s := range servers {
			text, err := manager.StartServer(ctx, s.ID)
			if err != nil {
				skipped = append(skipped, s.DisplayName+": "+friendlyStartError(err))
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
			lines = append(lines, "🚀 <b>Запуск запрошен:</b> "+strings.Join(started, ", "))
		}
		if len(skipped) > 0 {
			lines = append(lines, "⏳ <b>Пропущены:</b> "+strings.Join(skipped, "; "))
		}
		if len(lines) == 0 {
			lines = append(lines, "Ничего не запущено.")
		}
		_, err := bot.b.Send(m.Chat, strings.Join(lines, "\n"))
		return err
	})
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

		var ownerID int64
		var servers []*database.Server
		chatIDs := []int64{}

		if bot.isPrivate(c) {
			isOwner, _ := bot.db.IsOwner(uid)
			if !isOwner {
				_, err := bot.b.Send(m.Chat, "Сначала пройдите онбординг: /start.")
				return err
			}
			if bot.lockdownActive(uid) {
				_, _ = bot.b.Send(m.Chat, msgLockdownBlocked)
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
				_, _ = bot.b.Send(m.Chat, msgLockdownBlocked)
				return nil
			}
			if !bot.canManage(uid, chatID) {
				_, err := bot.b.Send(m.Chat, "У вас нет доступа к серверам этого чата.")
				return err
			}
			ownerID = owner.UserID
			servers, _ = bot.db.GetChatServers(chatID)
			chatIDs = append(chatIDs, chatID)
		} else {
			return nil
		}

		if len(servers) == 0 {
			_, err := bot.b.Send(m.Chat, "Нет подключённых серверов.")
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
			lines = append(lines, "✅ <b>Запуск подтверждён:</b> "+strings.Join(confirmed, ", "))
		}
		if len(skipped) > 0 {
			lines = append(lines, "⏳ <b>Не подтверждены (нет очереди/ошибка):</b> "+strings.Join(skipped, ", "))
		}
		if len(lines) == 0 {
			lines = append(lines, "Ничего не подтверждено.")
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

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, "Чат не привязан к владельцу.", false)
		return nil
	}
	if bot.lockdownBlocked(c, owner.UserID) {
		return nil
	}
	if !bot.canManage(uid, chatID) {
		bot.answer(c, "У вас нет доступа к серверам этого чата.", false)
		return nil
	}
	server, _ := bot.db.GetServer(serverID)
	if server == nil || server.OwnerID != owner.UserID {
		bot.answer(c, "Сервер не найден.", false)
		return nil
	}

	manager := bot.managers.For(owner.UserID)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	text, err := manager.StartServer(ctx, serverID)
	if err != nil {
		bot.answer(c, friendlyStartError(err), true)
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
		owner, _ := bot.db.GetChatOwner(chatID)
		if owner != nil && owner.UserID == uid {
			_, err := bot.b.Send(m.Chat,
				"<b>Команды владельца чата:</b>\n"+
					"/link — привязать чат (серверы выбираются в ЛС)\n"+
					"/unlink — отвязать чат\n"+
					"/status — статус серверов чата\n"+
					"/run — запустить серверы чата\n"+
					"/emergency — локдаун (в ЛС с ботом)\n\n"+
					"<b>Кнопки дашборда:</b>\n"+
					"▶️ Старт · ⏹ Стоп · ✅ Подтвердить (появляется, когда сервер в очереди)\n"+
					"Также доступно: /confirm — подтвердить очередь вручную")
			return err
		}
		_, err := bot.b.Send(m.Chat,
			"<b>Доступные действия:</b>\n"+
				"/status — статус серверов чата\n"+
				"/run — запустить серверы чата\n"+
				"🙋 Запросить доступ — кнопка под дашбордом\n\n"+
				"Управление серверами доступно владельцу и одобренным участникам.")
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
	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, "Чат не привязан к владельцу.", false)
		return 0, false
	}
	if owner.UserID == cb.Sender.ID {
		return owner.UserID, true
	}
	if owner.LockdownMode {
		bot.answer(c, msgLockdownBlocked, false)
		return 0, false
	}
	ok, _ := bot.db.UserCanManage(cb.Sender.ID, chatID)
	if !ok {
		bot.answer(c, "У вас нет доступа. Нажмите «🙋 Запросить доступ».", false)
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
	if cb.Message.Chat.Type == tele.ChatPrivate {
		bot.answer(c, "Кнопки дашборда работают в групповом чате.", false)
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
			bot.answer(c, friendlyStartError(err), true)
			return nil
		}
		bot.dash.MarkAsStarting(300)
		bot.answer(c, text, false)
		bot.watcher.Start(bot.b, ownerID, serverID)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
		bot.broadcastOthers(ownerID, chatID, "🚀 <b>"+text+"</b>")
	case "stop":
		text, err := manager.StopServer(ctx, serverID)
		if err != nil {
			bot.answer(c, err.Error(), true)
			return nil
		}
		bot.dash.ClearQueuePosition(serverID)
		bot.answer(c, text, false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
		bot.broadcastOthers(ownerID, chatID, "🛑 <b>"+text+"</b>")
	case "confirm":
		if bot.lockdownBlocked(c, ownerID) {
			return nil
		}
		if err := manager.ConfirmServer(ctx, serverID); err != nil {
			bot.answer(c, err.Error(), true)
			return nil
		}
		bot.dash.ClearQueuePosition(serverID)
		bot.answer(c, "✅ Запуск подтверждён.", false)
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	}
	return nil
}

// friendlyStartError — дружелюбное описание ошибки запуска: если сервер
// уже запускается или оффлайн, пользователю показывается нейтральное
// сообщение без сырых деталей и без каких-либо уведомлений Владельцу.
func friendlyStartError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "already"):
		return "⏳ Сервер уже запускается, ожидайте."
	case strings.Contains(msg, "cloudflare"):
		return "⚠️ Aternos временно ограничивает запросы, попробуйте позже."
	case strings.Contains(msg, "session"):
		return "⚠️ Сессия Aternos истекла, сообщите владельцу."
	}
	return "❌ Сервер недоступен, ожидайте."
}

func (bot *Bot) broadcastOthers(ownerID, exceptChatID int64, text string) {
	chats, _ := bot.db.GetChatsByOwner(ownerID)
	other := make([]int64, 0, len(chats))
	for _, ch := range chats {
		if ch.ChatID != exceptChatID {
			other = append(other, ch.ChatID)
		}
	}
	bot.dash.BroadcastMessage(bot.b, other, text)
}

func (bot *Bot) cbRefreshDashboard(c tele.Context) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false) // мгновенно убираем спиннер
	if cb.Message.Chat.Type == tele.ChatPrivate || cb.Sender == nil {
		return nil
	}
	chatID := cb.Message.Chat.ID
	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		return nil
	}
	if owner.UserID != cb.Sender.ID {
		ok, _ := bot.db.UserCanManage(cb.Sender.ID, chatID)
		if !ok {
			return nil // нет доступа — молча
		}
	}
	bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
	bot.answer(c, "🔄 Дашборд обновлён.", false)
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

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil {
		bot.answer(c, "Чат не привязан к владельцу.", false)
		return nil
	}
	if owner.UserID == uid {
		bot.answer(c, "Вы — владелец этого чата 😉", false)
		return nil
	}
	ok, _ := bot.db.UserCanManage(chatID, uid)
	if ok {
		bot.answer(c, "У вас уже есть доступ.", false)
		return nil
	}

	bot.mu.Lock()
	last := bot.lastAccessReq[uid]
	bot.mu.Unlock()
	if time.Since(last) < accessReqCooldown {
		bot.answer(c, "Запрос уже отправлен — подождите 5 минут.", false)
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
		"🙋 <b>Запрос доступа</b>\n"+
			"Чат: "+chatTitle+" ("+strconv.FormatInt(chatID, 10)+")\n"+
			"Пользователь: "+html.EscapeString(cb.Sender.FirstName+" "+cb.Sender.LastName)+
			" (@<code>"+html.EscapeString(cb.Sender.Username)+"</code>)",
		approveAccessKB(uid, chatID))
	if err != nil {
		log.Printf("не удалось доставить запрос владельцу %d: %v", owner.UserID, err)
		bot.answer(c, "Не удалось отправить запрос владельцу.", false)
		return nil
	}
	bot.answer(c, "Запрос отправлен владельцу.", false)
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

	owner, err := bot.db.GetChatOwner(chatID)
	if err != nil || owner == nil || owner.UserID != uid {
		bot.answer(c, "Только владелец чата может решать этот запрос.", false)
		return nil
	}

	if approve {
		_ = bot.db.SetUserAccess(userID, chatID, true)
		_ = bot.db.LogAction(owner.UserID, "access_grant",
			fmt.Sprintf("user %d", userID), userID, chatID, 0)
		_ = bot.edit(cb.Message, cb.Message.Text+"\n\n✅ <b>Доступ одобрен.</b>", nil)
		_, _ = bot.b.Send(&tele.Chat{ID: userID},
			"✅ Вам выдан доступ к серверам чата «"+cb.Message.Chat.Title+"».")
		_, _ = bot.b.Send(&tele.Chat{ID: chatID},
			"✅ Доступ одобрен. Пользователь может управлять серверами чата.")
	} else {
		_ = bot.db.LogAction(owner.UserID, "access_deny",
			fmt.Sprintf("user %d", userID), userID, chatID, 0)
		_ = bot.edit(cb.Message, cb.Message.Text+"\n\n❌ <b>Отклонено.</b>", nil)
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