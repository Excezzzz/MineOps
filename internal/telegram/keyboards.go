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
)

// Префиксы callback-данных (формат aiogram: "prefix:field1:field2",
// bool-поля кодируются как "True"/"False" — совместимо со старыми кнопками).
const (
	cbPanel       = "panel"         // panel:<action>
	cbPanelServer = "psrv"          // psrv:<server_id>
	cbPanelAction = "pact"          // pact:<server_id>:<action>
	cbPanelChat   = "pchat"         // pchat:<chat_id>:<action>
	cbPanelChatS  = "pcsrv"         // pcsrv:<chat_id>:<server_id>:<action>
	cbUsersPage   = "upage"         // upage:<chat_id>:<page>
	cbOwnerSet    = "oset"          // oset:<action>:<enabled True/False>
	cbDeleteAcc   = "delacc"        // delacc:<confirm True/False>
	cbServer      = "srv"           // srv:<server_id>:<action>
	cbReqAccess   = "req_acc"       // req_acc:<chat_id>
	cbApproveAcc  = "app_acc"       // app_acc:<user_id>:<chat_id>:<approve True/False>
	cbOnboarding  = "onb"           // onb:<action>:<aternos_id>
	cbRefreshDash = "refresh_dashboard"
	cbNoop        = "noop"
)

func cbData(parts ...string) string {
	return strings.Join(parts, ":")
}

// ------------------------------------------------------------------ //
// панель владельца
// ------------------------------------------------------------------ //

func ownerPanelKB() *tele.ReplyMarkup {
	rows := [][]tele.InlineButton{
		{{Text: "🖥 Серверы", Data: cbData(cbPanel, "servers")}},
		{{Text: "💬 Чаты", Data: cbData(cbPanel, "chats")}},
		{{Text: "⚙️ Настройки", Data: cbData(cbPanel, "settings")}},
		{{Text: "📋 Аудит", Data: cbData(cbPanel, "audit")}},
		{{Text: "🔄 Обновить серверы", Data: cbData(cbPanel, "refresh_servers")}},
		{{Text: "🔄 Обновить куку", Data: cbData(cbPanel, "refresh_session")}},
		{{Text: "🗑 Удалить аккаунт", Data: cbData(cbPanel, "delete_account")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerServersKB(servers []*database.Server) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		rows = append(rows, []tele.InlineButton{{
			Text: "🖥 " + s.DisplayName,
			Data: cbData(cbPanelServer, strconv.FormatInt(s.ID, 10)),
		}})
	}
	if len(servers) == 0 {
		rows = append(rows, []tele.InlineButton{{Text: "(серверов нет)", Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{Text: "🔙 Назад", Data: cbData(cbPanel, "back")}})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func serverCardKB(serverID int64, isOnline bool) *tele.ReplyMarkup {
	actionRow := []tele.InlineButton{}
	if isOnline {
		actionRow = append(actionRow, tele.InlineButton{
			Text: "⏹ Стоп", Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "stop"),
		})
	} else {
		actionRow = append(actionRow,
			tele.InlineButton{
				Text: "▶️ Старт", Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "start"),
			},
			tele.InlineButton{
				Text: "✅ Подтвердить", Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "confirm"),
			},
		)
	}
	rows := [][]tele.InlineButton{
		actionRow,
		{
			{Text: "🗑 Удалить", Data: cbData(cbPanelAction, strconv.FormatInt(serverID, 10), "delete")},
			{Text: "🔙 К серверам", Data: cbData(cbPanel, "servers")},
		},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerChatsKB(chats []*database.Chat) *tele.ReplyMarkup {
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
		rows = append(rows, []tele.InlineButton{{Text: "(чатов нет)", Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{Text: "🔙 Назад", Data: cbData(cbPanel, "back")}})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func chatCardKB(chatID int64, servers []*database.Server, linkedIDs map[int64]bool) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+3)
	for _, s := range servers {
		linked := linkedIDs[s.ID]
		text := "☑️ " + s.DisplayName
		action := "bind"
		if linked {
			text = "✅ " + s.DisplayName
			action = "unbind"
		}
		rows = append(rows, []tele.InlineButton{{
			Text: text,
			Data: cbData(cbPanelChatS, strconv.FormatInt(chatID, 10),
				strconv.FormatInt(s.ID, 10), action),
		}})
	}
	if len(servers) == 0 {
		rows = append(rows, []tele.InlineButton{{Text: "(серверов нет)", Data: cbNoop}})
	}
	rows = append(rows, []tele.InlineButton{{
		Text: "👥 Участники", Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "users"),
	}})
	rows = append(rows, []tele.InlineButton{
		{Text: "🗑 Отвязать чат", Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "unlink")},
		{Text: "🔙 К чатам", Data: cbData(cbPanel, "chats")},
	})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func usersPageKB(chatID int64, page, total, limit int) *tele.ReplyMarkup {
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	nav := []tele.InlineButton{}
	if page > 0 {
		nav = append(nav, tele.InlineButton{
			Text: "⬅️ Назад",
			Data: cbData(cbUsersPage, strconv.FormatInt(chatID, 10), strconv.Itoa(page-1)),
		})
	}
	nav = append(nav, tele.InlineButton{
		Text: fmt.Sprintf("%d/%d", page+1, pages), Data: cbNoop,
	})
	if page+1 < pages {
		nav = append(nav, tele.InlineButton{
			Text: "➡️ Вперёд",
			Data: cbData(cbUsersPage, strconv.FormatInt(chatID, 10), strconv.Itoa(page+1)),
		})
	}
	rows := [][]tele.InlineButton{
		nav,
		{{Text: "🔙 К чату", Data: cbData(cbPanelChat, strconv.FormatInt(chatID, 10), "select")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerSettingsKB(lockdown, autoConfirm bool) *tele.ReplyMarkup {
	acText := "❌ Автоподтверждение"
	if autoConfirm {
		acText = "✅ Автоподтверждение"
	}
	ldText := "🔓 Локдаун выкл"
	if lockdown {
		ldText = "🔒 Локдаун вкл"
	}
	rows := [][]tele.InlineButton{
		{{Text: acText, Data: cbData(cbOwnerSet, "auto_confirm", boolStr(!autoConfirm))}},
		{{Text: ldText, Data: cbData(cbOwnerSet, "lockdown", boolStr(!lockdown))}},
		{{Text: "↩️ Назад", Data: cbData(cbPanel, "back")}},
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func ownerAuditKB() *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{{Text: "🔙 Назад", Data: cbData(cbPanel, "back")}},
	}}
}

func deleteAccountKB() *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{
			{Text: "🗑 Да, удалить всё", Data: cbData(cbDeleteAcc, boolStr(true))},
			{Text: "↩️ Отмена", Data: cbData(cbDeleteAcc, boolStr(false))},
		},
	}}
}

// ------------------------------------------------------------------ //
// групповой дашборд
// ------------------------------------------------------------------ //

// DashboardKB — фабрика клавиатуры дашборда (инжектится в dashboard.New).
func DashboardKB(servers []dashboard.DashServer, chatID int64) *tele.ReplyMarkup {
	return dashboardKB(servers, chatID)
}

// dashboardKB — кнопки дашборда: по строке на сервер + обновление/доступ.
func dashboardKB(servers []dashboard.DashServer, chatID int64) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		name := s.DisplayName
		if name == "" {
			name = fmt.Sprintf("ID %d", s.ID)
		}
		row := []tele.InlineButton{{
			Text: "🖥 " + name,
			Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "status"),
		}}
		if s.IsOnline {
			row = append(row, tele.InlineButton{
				Text: "⏹ Стоп", Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "stop"),
			})
		} else {
			row = append(row,
				tele.InlineButton{
					Text: "▶️ Старт", Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "start"),
				},
				tele.InlineButton{
					Text: "✅ Подтвердить", Data: cbData(cbServer, strconv.FormatInt(s.ID, 10), "confirm"),
				},
			)
		}
		rows = append(rows, row)
	}
	rows = append(rows, []tele.InlineButton{
		{Text: "🔄 Обновить", Data: cbRefreshDash},
		{Text: "🙋 Запросить доступ", Data: cbData(cbReqAccess, strconv.FormatInt(chatID, 10))},
	})
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

func requestAccessKB(chatID int64) *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{{Text: "🙋 Запросить доступ", Data: cbData(cbReqAccess, strconv.FormatInt(chatID, 10))}},
	}}
}

func approveAccessKB(userID, chatID int64) *tele.ReplyMarkup {
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{
		{
			{Text: "✅ Одобрить", Data: cbData(cbApproveAcc,
				strconv.FormatInt(userID, 10), strconv.FormatInt(chatID, 10), boolStr(true))},
			{Text: "❌ Отклонить", Data: cbData(cbApproveAcc,
				strconv.FormatInt(userID, 10), strconv.FormatInt(chatID, 10), boolStr(false))},
		},
	}}
}

// ------------------------------------------------------------------ //
// онбординг
// ------------------------------------------------------------------ //

func serverPickerKB(servers []aternos.ServerBrief, selected map[string]bool, maxServers int) *tele.ReplyMarkup {
	rows := make([][]tele.InlineButton, 0, len(servers)+1)
	for _, s := range servers {
		name := s.DisplayName
		if name == "" {
			name = "ID " + s.AternosID
		}
		checked := "⬜"
		if selected[s.AternosID] {
			checked = "✅"
		}
		rows = append(rows, []tele.InlineButton{{
			Text: checked + " " + name,
			Data: cbData(cbOnboarding, "toggle", s.AternosID),
		}})
	}
	rows = append(rows, []tele.InlineButton{{
		Text: fmt.Sprintf("Готово (%d/%d)", len(selected), maxServers),
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