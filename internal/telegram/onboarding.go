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
	"mineops/internal/i18n"
)

func (bot *Bot) startOnboarding(c tele.Context) {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return
	}
	lang := i18n.Detect(m.Sender.LanguageCode)
	bot.fsm.Set(m.Sender.ID, fsmOnbWaitingCookie)
	bot.fsm.SetData(m.Sender.ID, "username", m.Sender.Username)
	bot.fsm.SetData(m.Sender.ID, "full_name", m.Sender.FirstName+" "+m.Sender.LastName)
	_, _ = bot.b.Send(m.Chat, i18n.T(lang, "onb_welcome"))
}

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

	lang := i18n.Detect(m.Sender.LanguageCode)
	statusMsg, err := bot.b.Send(m.Chat, i18n.T(lang, "onb_checking"))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	servers, err := bot.managers.For(m.Sender.ID).ProbeSession(ctx, cookie)
	if err != nil {
		_, _ = bot.b.Edit(statusMsg, i18n.T(lang, "err_prefix", err.Error()))
		return
	}
	if len(servers) == 0 {
		_, _ = bot.b.Edit(statusMsg, i18n.T(lang, "onb_no_servers"))
		return
	}
	bot.fsm.Set(m.Sender.ID, fsmOnbSelecting)
	bot.fsm.SetData(m.Sender.ID, "cookie", cookie)
	bot.fsm.SetData(m.Sender.ID, "servers", servers)
	bot.fsm.SetData(m.Sender.ID, "selected", map[string]bool{})
	bot.fsm.SetData(m.Sender.ID, "page", 0)
	_, _ = bot.b.Edit(statusMsg,
		i18n.T(lang, "onb_valid"),
		serverPickerKB(servers, map[string]bool{}, 0, lang))
}

func (bot *Bot) cbOnboarding(c tele.Context, parts []string) error {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Sender == nil {
		return c.Respond(&tele.CallbackResponse{})
	}
	bot.answer(c, "", false)
	action := cbStr(parts, 1)
	uid := cb.Sender.ID
	lang := i18n.Detect(cb.Sender.LanguageCode)

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
		_ = bot.edit(cb.Message, i18n.T(lang, "onb_valid"),
			serverPickerKB(servers, selected, page, lang))
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
		_ = bot.edit(cb.Message, i18n.T(lang, "onb_valid"),
			serverPickerKB(servers, selected, newPage, lang))
		return nil
	}

	if action == "done" {
		selAny, _ := bot.fsm.GetData(uid, "selected")
		selected, ok := selAny.(map[string]bool)
		if !ok {
			bot.answer(c, i18n.T(lang, "onb_session_expired"), false)
			return nil
		}
		if len(selected) == 0 {
			bot.answer(c, i18n.T(lang, "onb_select_any"), false)
			return nil
		}
		cookieAny, ok := bot.fsm.GetData(uid, "cookie")
		if !ok {
			bot.answer(c, i18n.T(lang, "onb_session_expired"), false)
			return nil
		}
		cookie := cookieAny.(string)
		serversAny, _ := bot.fsm.GetData(uid, "servers")
		servers := serversAny.([]aternos.ServerBrief)

		_ = bot.edit(cb.Message, i18n.T(lang, "onb_saving"), nil)
		if err := bot.db.CreateOwner(uid,
			cb.Sender.Username, cb.Sender.FirstName+" "+cb.Sender.LastName,
			crypto.EncryptSession(cookie)); err != nil {
			log.Printf("onboarding: failed to create owner %d: %v", uid, err)
			bot.answer(c, i18n.T(lang, "onb_create_fail"), true)
			return nil
		}
		_ = bot.db.SetOwnerLang(uid, lang)
		added := []string{}
		for _, s := range servers {
			if selected[s.AternosID] {
				id, err := bot.db.AddServer(uid, s.AternosID, s.ServerIP, s.DisplayName)
				if err == nil && id > 0 {
					added = append(added, s.DisplayName)
				}
			}
		}
		_ = bot.db.LogAction(uid, "onboarding", "servers: "+strings.Join(added, ", "), 0, 0, 0)
		bot.fsm.Clear(uid)

		addedText := i18n.T(lang, "none_short")
		if len(added) > 0 {
			addedText = strings.Join(added, ", ")
		}
		_ = bot.edit(cb.Message, i18n.T(lang, "onb_done", addedText), nil)
	}
	return nil
}
