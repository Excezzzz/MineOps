// Package telegram — бот: хэндлеры, клавиатуры, FSM, мидлвари.
package telegram

import (
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"mineops/internal/aternos"
	"mineops/internal/dashboard"
	"mineops/internal/database"
	"mineops/internal/i18n"
)

// Префиксы callback-данных (формат aiogram: "prefix:field1:field2",
// bool-поля кодируются как "True"/"False" — совместимо со старыми кнопками).
const (
	cbPanel       = "panel"   // panel:<action>
	cbPanelServer = "psrv"    // psrv:<server_id>
	cbPanelAction = "pact"    // pact:<server_id>:<action>
	cbPanelChat   = "pchat"   // pchat:<chat_id>:<action>
	cbPanelChatS  = "pcsrv"   // pcsrv:<chat_id>:<server_id>:<action>
	cbUsersPage   = "upage"   // upage:<chat_id>:<page>
	cbOwnerSet    = "oset"    // oset:<action>:<enabled True/False>
	cbDeleteAcc   = "delacc"  // delacc:<confirm True/False>
	cbServer      = "srv"     // srv:<server_id>:<action>
	cbReqAccess   = "req_acc" // req_acc:<chat_id>
	cbApproveAcc  = "app_acc" // app_acc:<user_id>:<chat_id>:<approve True/False>
	cbRunSrv      = "run_srv" // run_srv:<server_id>:<chat_id> — выбор сервера для /run
	cbOnboarding  = "onb"     // onb:<action>:<aternos_id>
	cbRefreshDash = "refresh_dashboard"
	cbNoop        = "noop"
)

func cbData(parts ...string) string {
	return strings.Join(parts, ":")
}

// btnName укорачивает длинное имя сервера, чтобы кнопка не выходила за
// границы сообщения (Telegram не переносит текст кнопки красиво).
func btnName(name string) string {
	const maxLen = 24
	r := []rune(name)
	if len(r) <= maxLen {
		return name
	}
	return string(r[:maxLen-1]) + "…"
}

// ------------------------------------------------------------------ //
// панель владельца
// ------------------------------------------------------------------ //

func ownerPanelKB(lang string) *tele.ReplyMarkup {
	rows := [][]tele.InlineButton{
		{{Text: i18n.T(lang, "btn_servers"), Data: cbData(cbPanel, "servers")}},
		{{Text: i18n.T(lang, "btn_chats"), Data: cbData(cbPanel, "chats")}},
		{{Text: i18n.T(lang, "btn_settings"), Data: cbData(cbPanel, "settings")}},
		{{Text: i18n.T(lang, "btn_audit"), Data: cbData(cbPanel, "audit")}},
		{{Text: i18n.T(lang, "btn_refresh_servers"), Data: cbData(cbPanel, "refresh_servers")}},
		{{Text: i18n.T(lang, "btn_refresh_cookie"), Data: cbData(cbPanel, "refresh_session")}},
		{{Text: i18n.T(lang, "btn_delete_account"), Data: cbData(cbPanel, "delete_account")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerServersKB(servers []*database.Server, lang string) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		rows = append(rows, []tele.InlineButton{{
			Text: "🖥 " + btnName(s.DisplayName),
			Data: cbData(cbPanelServer, strconv.FormatInt(s.ID, 10)),
		}})
	}
	if len(servers) == 0 {
		rows = append(rows, []tele.InlineButton{{Text: i18n.T(lang, "btn_no_servers"), Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{Text: i18n.T(lang, "btn_back"), Data: cbData(cbPanel, "back")}})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func serverCardKB(serverID int64, isOnline bool, lang string) *tele.ReplyMarkup {
	actionRow := []tele.InlineButton{}
	if isOnline {
		actionRow = append(actionRow, tele.InlineButton{
			Text: i18n.T(lang, "btn_stop"), Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "stop"),
		})
	} else {
		actionRow = append(actionRow,
			tele.InlineButton{
				Text: i18n.T(lang, "btn_start"), Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "start"),
			},
			tele.InlineButton{
				Text: i18n.T(lang, "btn_confirm"), Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "confirm"),
			},
		)
	}
	rows := [][]tele.InlineButton{
		actionRow,
		{
			{Text: i18n.T(lang, "btn_delete"), Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "delete")},
			{Text: i18n.T(lang, "btn_to_servers"), Data: cbData(cbPanel, "servers")},
		},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerChatsKB(chats []*database.Chat, lang string) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(chats)+1)
	for _, ch := range chats {
		title := ch.Title
		if title == "" {
			title = strconv.FormatInt(ch.ChatID, 10)
		}
		rows = append(rows, []tele.InlineButton{{
			Text: "💬 " + title,
			Data: cbData(cbPanelChat, strconv.FormatInt(ch.ChatID, 10), "select"),
		}})
	}
	if len(chats) == 0 {
		rows = append(rows, []tele.InlineButton{{Text: i18n.T(lang, "btn_no_chats"), Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{Text: i18n.T(lang, "btn_back"), Data: cbData(cbPanel, "back")}})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func chatCardKB(chatID int64, servers []*database.Server, linkedIDs map[int64]bool, lang string) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+3)
	for _, s := range servers {
		linked := linkedIDs[s.ID]
		text := "☑️ " + btnName(s.DisplayName)
		action := "bind"
		if linked {
			text = "✅ " + btnName(s.DisplayName)
			action = "unbind"
		}
		rows = append(rows, []tele.InlineButton{{
			Text: text,
			Data: cbData(cbPanelChatS, strconv.FormatInt(chatID, 10),
				strconv.FormatInt(s.ID, 10), action),
		}})
	}
	if len(servers) == 0 {
		rows = append(rows, []tele.InlineButton{{Text: i18n.T(lang, "btn_no_servers"), Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{
		Text: i18n.T(lang, "btn_users"), Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "users"),
	}})
	rows = append(rows, []tele.InlineButton{
		{Text: i18n.T(lang, "btn_unlink_chat"), Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "unlink")},
		{Text: i18n.T(lang, "btn_to_chats"), Data: cbData(cbPanel, "chats")},
	})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func usersPageKB(chatID int64, page, total, limit int, lang string) *tele.ReplyMarkup {
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	nav := []tele.InlineButton{}
	if page > 0 {
		nav = append(nav, tele.InlineButton{
			Text: i18n.T(lang, "btn_prev"),
			Data: cbData(cbUsersPage, strconv.FormatInt(chatID, 10), strconv.Itoa(page-1)),
		})
	}
	nav = append(nav, tele.InlineButton{
		Text: fmt.Sprintf("%d/%d", page+1, pages), Data: cbNoop,
	})
	if page+1 < pages {
		nav = append(nav, tele.InlineButton{
			Text: i18n.T(lang, "btn_next"),
			Data: cbData(cbUsersPage, strconv.FormatInt(chatID, 10), strconv.Itoa(page+1)),
		})
	}
	rows := [][]tele.InlineButton{
		nav,
		{{Text: i18n.T(lang, "btn_to_chat"), Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "select")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerSettingsKB(lockdown, autoConfirm bool, lang string) *tele.ReplyMarkup {
	acText := i18n.T(lang, "btn_ac")
	ldText := i18n.T(lang, "btn_ld_off")
	if lockdown {
		ldText = i18n.T(lang, "btn_ld_on")
	}
	nextLang := "ru"
	if lang == "ru" {
		nextLang = "en"
	}
	rows := [][]tele.InlineButton{
		{{Text: acText, Data: cbData(cbOwnerSet, "auto_confirm", boolStr(!autoConfirm))}},
		{{Text: ldText, Data: cbData(cbOwnerSet, "lockdown", boolStr(!lockdown))}},
		{{Text: i18n.T(lang, "btn_lang", strings.ToUpper(lang)),
			Data: cbData(cbOwnerSet, "lang", boolStr(nextLang == "en"))}},
		{{Text: i18n.T(lang, "btn_back"), Data: cbData(cbPanel, "back")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerAuditKB(lang string) *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{{Text: i18n.T(lang, "btn_back"), Data: cbData(cbPanel, "back")}},
	}}
}

func deleteAccountKB(lang string) *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{
			{Text: i18n.T(lang, "btn_yes_delete"), Data: cbData(cbDeleteAcc, boolStr(true))},
			{Text: i18n.T(lang, "btn_cancel"), Data: cbData(cbDeleteAcc, boolStr(false))},
		},
	}}
}

// ------------------------------------------------------------------ //
// групповой дашборд
// ------------------------------------------------------------------ //

// DashboardKB — фабрика клавиатуры дашборда (инжектится в dashboard.New).
func DashboardKB(servers []dashboard.DashServer, chatID int64, lang string) *tele.ReplyMarkup {
	return dashboardKB(servers, chatID, lang)
}

// dashboardKB — кнопки дашборда: по строке действий на сервер + обновление/доступ.
// «✅ Подтвердить» появляется ТОЛЬКО в статусе is_starting (сервер в очереди).
func dashboardKB(servers []dashboard.DashServer, chatID int64, lang string) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		var row []tele.InlineButton
		if s.IsOnline {
			row = []tele.InlineButton{{
				Text: i18n.T(lang, "btn_stop"), Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "stop"),
			}}
		} else if s.Starting {
			row = []tele.InlineButton{
				{
					Text: i18n.T(lang, "btn_confirm"), Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "confirm"),
				},
				{
					Text: i18n.T(lang, "btn_start"), Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "start"),
				},
			}
		} else {
			row = []tele.InlineButton{{
				Text: i18n.T(lang, "btn_start"), Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "start"),
			}}
		}
		rows = append(rows, row)
	}
	rows = append(rows, []tele.InlineButton{
		{Text: i18n.T(lang, "btn_refresh"), Data: cbRefreshDash},
		{Text: i18n.T(lang, "btn_request_access"), Data: cbData(cbReqAccess, strconv.FormatInt(chatID, 10))},
	})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func approveAccessKB(userID, chatID int64, lang string) *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{
			{Text: i18n.T(lang, "btn_approve"), Data: cbData(cbApproveAcc,
				strconv.FormatInt(userID, 10), strconv.FormatInt(chatID, 10), boolStr(true))},
			{Text: i18n.T(lang, "btn_deny"), Data: cbData(cbApproveAcc,
				strconv.FormatInt(userID, 10), strconv.FormatInt(chatID, 10), boolStr(false))},
		},
	}}
}

// runServerPickerKB — выбор сервера для запуска через /run (несколько серверов).
func runServerPickerKB(servers []*database.Server, chatID int64, lang string) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		name := s.DisplayName
		if name == "" {
			name = fmt.Sprintf("ID %d", s.ID)
		}
		rows = append(rows, []tele.InlineButton{{
			Text: i18n.T(lang, "btn_start") + " " + btnName(name),
			Data: cbData(cbRunSrv, strconv.FormatInt(s.ID, 10), strconv.FormatInt(chatID, 10)),
		}})
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

// ------------------------------------------------------------------ //
// онбординг
// ------------------------------------------------------------------ //

const onbPageSize = 8

// serverPickerKB — чекбоксы выбора серверов с пагинацией (по 8 на страницу).
func serverPickerKB(servers []aternos.ServerBrief, selected map[string]bool, page int, lang string) *tele.ReplyMarkup {
	total := len(servers)
	pages := (total + onbPageSize - 1) / onbPageSize
	if pages < 1 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * onbPageSize
	end := start + onbPageSize
	if end > total {
		end = total
	}
	rows := make([][]tele.InlineButton, 0, onbPageSize+2)
	for _, s := range servers[start:end] {
		name := s.DisplayName
		if name == "" {
			name = "ID " + s.AternosID
		}
		checked := "⬜"
		if selected[s.AternosID] {
			checked = "✅"
		}
		rows = append(rows, []tele.InlineButton{{
			Text: checked + " " + btnName(name),
			Data: cbData(cbOnboarding, "toggle", s.AternosID),
		}})
	}
	if total > onbPageSize {
		nav := []tele.InlineButton{}
		if page > 0 {
			nav = append(nav, tele.InlineButton{
				Text: "⬅️",
				Data: cbData(cbOnboarding, "page", strconv.Itoa(page-1)),
			})
		}
		nav = append(nav, tele.InlineButton{
			Text: fmt.Sprintf("%d/%d", page+1, pages), Data: cbNoop,
		})
		if end < total {
			nav = append(nav, tele.InlineButton{
				Text: "➡️",
				Data: cbData(cbOnboarding, "page", strconv.Itoa(page+1)),
			})
		}
		rows = append(rows, nav)
	}
	rows = append(rows, []tele.InlineButton{{
		Text: i18n.T(lang, "btn_done", len(selected)),
		Data: cbData(cbOnboarding, "done", ""),
	}})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

// ------------------------------------------------------------------ //
// парсинг callback-данных
// ------------------------------------------------------------------ //

// splitCb разбивает "prefix:a:b" на [prefix, a, b].
func splitCb(data string) []string {
	return strings.Split(data, ":")
}

func cbInt(parts []string, idx int) int64 {
	if idx >= len(parts) {
		return 0
	}
	n, _ := strconv.ParseInt(parts[idx], 10, 64)
	return n
}

func cbStr(parts []string, idx int) string {
	if idx >= len(parts) {
		return ""
	}
	return parts[idx]
}

func boolStr(b bool) string {
	if b {
		return "True" // aiogram-стиль: совместимость со старыми кнопками
	}
	return "False"
}

func parseBool(s string) bool {
	return s == "True" || s == "1"
}