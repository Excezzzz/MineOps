package telegram

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/aternos"
	"mineops/internal/crypto"
)

// startOnboarding — начало онбординга (вызывается из /start).
func (bot *Bot) startOnboarding(c tele.Context) {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return
	}
	bot.fsm.Set(m.Sender.ID, fsmOnbWaitingCookie)
	bot.fsm.SetData(m.Sender.ID, "username", m.Sender.Username)
	bot.fsm.SetData(m.Sender.ID, "full_name", m.Sender.FirstName+" "+m.Sender.LastName)
	_, _ = bot.b.Send(m.Chat,
		"👋 <b>Привет! Я MineOps — бот для управления серверами Aternos.</b>\n\n"+
			"Отправьте куку <code>ATERNOS_SESSION</code> — я проверю её, и вы сможете "+
			"управлять своими серверами прямо в Telegram.\n\n"+
			"💡 <b>Как получить куку:</b> зайдите на aternos.org → откройте консоль "+
			"браузера (F12) → вкладка Network → обновите страницу → найдите запрос "+
			"на ваш аккаунт → закладка Cookies → скопируйте значение "+
			"<code>ATERNOS_SESSION</code>.\n\n"+
			"<i>🔒 Кука шифруется (Fernet) и хранится только в вашем профиле бота. "+
			"Сообщение с кукой сразу удаляется.</i>")
}

// onSessionCookie — принимает куку в состоянии waiting_cookie.
func (bot *Bot) onSessionCookie(c tele.Context) {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return
	}
	cookie := decodeCookie(m.Text)
	if cookie == "" {
		return
	}
	// Мгновенно удаляем сообщение с кукой.
	_ = bot.b.Delete(m)

	statusMsg, err := bot.b.Send(m.Chat, "⏳ Проверяю сессию Aternos и ищу серверы...")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	servers, err := bot.managers.For(m.Sender.ID).ProbeSession(ctx, cookie)
	if err != nil {
		_, _ = bot.b.Edit(statusMsg, "❌ "+err.Error())
		return
	}
	if len(servers) == 0 {
		_, _ = bot.b.Edit(statusMsg,
			"На аккаунте Aternos не найдено серверов. Проверьте аккаунт и попробуйте снова.")
		return
	}
	bot.fsm.Set(m.Sender.ID, fsmOnbSelecting)
	bot.fsm.SetData(m.Sender.ID, "cookie", cookie)
	bot.fsm.SetData(m.Sender.ID, "servers", servers)
	bot.fsm.SetData(m.Sender.ID, "selected", map[string]bool{})
	bot.fsm.SetData(m.Sender.ID, "page", 0)
	_, _ = bot.b.Edit(statusMsg,
		"✅ Сессия действительна! Выберите серверы для управления:",
		serverPickerKB(servers, map[string]bool{}, 0))
}

// cbOnboarding — чекбоксы выбора серверов, пагинация и кнопка «Готово».
func (bot *Bot) cbOnboarding(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	action := cbStr(parts, 1)
	uid := cb.Sender.ID

	pageAny, _ := bot.fsm.GetData(uid, "page")
	page, _ := pageAny.(int)

	if action == "toggle" {
		sid := cbStr(parts, 2)
		data, ok := bot.fsm.GetData(uid, "servers")
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}
		servers, ok := data.([]aternos.ServerBrief)
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}
		selAny, _ := bot.fsm.GetData(uid, "selected")
		selected, ok := selAny.(map[string]bool)
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}

		if selected[sid] {
			delete(selected, sid)
		} else {
			selected[sid] = true
		}
		bot.fsm.SetData(uid, "selected", selected)
		_ = bot.edit(cb.Message, "✅ Сессия действительна! Выберите серверы:",
			serverPickerKB(servers, selected, page))
		return nil
	}

	if action == "page" {
		newPage := 0
		if n, err := strconv.Atoi(cbStr(parts, 2)); err == nil {
			newPage = n
		}
		data, ok := bot.fsm.GetData(uid, "servers")
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}
		servers, ok := data.([]aternos.ServerBrief)
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}
		selAny, _ := bot.fsm.GetData(uid, "selected")
		selected, ok := selAny.(map[string]bool)
		if !ok {
			return c.Respond(&tele.CallbackResponse{})
		}
		bot.fsm.SetData(uid, "page", newPage)
		_ = bot.edit(cb.Message, "✅ Сессия действительна! Выберите серверы:",
			serverPickerKB(servers, selected, newPage))
		return nil
	}

	if action == "done" {
		selAny, _ := bot.fsm.GetData(uid, "selected")
		selected, ok := selAny.(map[string]bool)
		if !ok {
			bot.answer(c, "Сессия устарела — начните заново /start.", false)
			return nil
		}
		if len(selected) == 0 {
			bot.answer(c, "Выберите хотя бы один сервер.", false)
			return nil
		}
		cookieAny, ok := bot.fsm.GetData(uid, "cookie")
		if !ok {
			bot.answer(c, "Сессия устарела — начните заново /start.", false)
			return nil
		}
		cookie := cookieAny.(string)
		serversAny, _ := bot.fsm.GetData(uid, "servers")
		servers := serversAny.([]aternos.ServerBrief)

		_ = bot.edit(cb.Message, "💾 Сохраняю аккаунт...", nil)
		if err := bot.db.CreateOwner(uid,
			cb.Sender.Username, cb.Sender.FirstName+" "+cb.Sender.LastName,
			crypto.EncryptSession(cookie)); err != nil {
			log.Printf("onboarding: создание владельца %d не удалось: %v", uid, err)
			bot.answer(c, "❌ Не удалось создать аккаунт. Попробуйте /start заново.", true)
			return nil
		}
		added := []string{}
		for _, s := range servers {
			if selected[s.AternosID] {
				id, err := bot.db.AddServer(uid, s.AternosID, s.ServerIP, s.DisplayName)
				if err == nil && id > 0 {
					added = append(added, s.DisplayName)
				}
			}
		}
		_ = bot.db.LogAction(uid, "onboarding", "серверы: "+strings.Join(added, ", "), 0, 0, 0)
		bot.fsm.Clear(uid)

		addedText := "нет"
		if len(added) > 0 {
			addedText = strings.Join(added, ", ")
		}
		_ = bot.edit(cb.Message,
			"🎉 <b>Готово!</b> Ваш аккаунт создан.\n\n"+
				"1️⃣ Добавьте бота в свою группу и выдайте ему права администратора;\n"+
				"2️⃣ В группе напишите <b>/link</b>;\n"+
				"3️⃣ В ЛС: /panel → 💬 Чаты → подключите нужные серверы для группы;\n"+
				"4️⃣ Дашборд со статусами закрепится в чате автоматически.\n\n"+
				"Подключено серверов: <b>"+addedText+"</b>\n\n"+
				"Команды: /panel — панель владельца, /set_session — обновить куку.", nil)
	}
	return nil
}
