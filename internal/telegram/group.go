package telegram

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"time"

	tele "gopkg.in/telebot.v3"
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
		if m == nil || m.Sender == nil || !bot.isGroup(c) {
			return nil
		}
		uid := m.Sender.ID
		chatID := m.Chat.ID
		if !bot.canManage(uid, chatID) {
			return nil
		}
		bot.dash.UpdateChatsDashboards(context.Background(), bot.b, []int64{chatID})
		bot.toast(c, "🔄 Дашборд обновлён.", 3)
		return nil
	})
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
					"/status — обновить дашборд вручную\n"+
					"/emergency — локдаун (в ЛС с ботом)\n\n"+
					"<b>Кнопки дашборда:</b>\n"+
					"▶️ Старт · ⏹ Стоп · ✅ Подтвердить очередь")
			return err
		}
		_, err := bot.b.Send(m.Chat,
			"<b>Доступные действия:</b>\n"+
				"/status — обновить дашборд\n"+
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
	ok, _ := bot.db.UserCanManage(chatID, uid)
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
		bot.answer(c, "🔒 Локдаун активен: управление только у владельца.", false)
		return 0, false
	}
	ok, _ := bot.db.UserCanManage(chatID, cb.Sender.ID)
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
		text, err := manager.StartServer(ctx, serverID)
		if err != nil {
			bot.answer(c, err.Error(), true)
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
		ok, _ := bot.db.UserCanManage(chatID, cb.Sender.ID)
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
		_ = bot.db.SetUserAccess(chatID, userID, true)
		_ = bot.db.LogAction(owner.UserID, "access_grant",
			fmt.Sprintf("user %d", userID), userID, chatID, 0)
		_ = bot.edit(cb.Message, cb.Message.Text+"\n\n✅ <b>Доступ одобрен.</b>", nil)
		_, _ = bot.b.Send(&tele.Chat{ID: userID},
			"✅ Вам выдан доступ к серверам чата «"+cb.Message.Chat.Title+"».")
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