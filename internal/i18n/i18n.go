// Package i18n — минималистичный переводчик UI-строк бота (ru/en).
//
// Все пользовательские строки (кнопки, сообщения, уведомления, тексты
// дашборда) должны лежать в этом словаре. Язык определяется автоматически
// из Telegram LanguageCode пользователя (при онбординге) и хранится в
// owners.lang; по умолчанию — ru.
package i18n

import "fmt"

// Detect нормализует LanguageCode Telegram до "ru" или "en".
func Detect(langCode string) string {
	switch {
	case len(langCode) >= 2 && (langCode[:2] == "ru" || langCode[:2] == "uk" || langCode[:2] == "be"):
		return "ru"
	}
	return "en"
}

// T возвращает локализованную строку по ключу. Аргументы подставляются
// через fmt.Sprintf (позиционные %s/%d). При неизвестном ключе/языке —
// откат на русский, затем на сам ключ.
func T(lang, key string, args ...any) string {
	tpl, ok := pick(lang)[key]
	if !ok {
		if tpl, ok = ru[key]; !ok {
			return key
		}
	}
	if len(args) == 0 {
		return tpl
	}
	return fmt.Sprintf(tpl, args...)
}

func pick(lang string) map[string]string {
	if lang == "ru" {
		return ru
	}
	return en
}

var ru = map[string]string{
	// --- common ---
	"lockdown_blocked":  "🚨 Включена экстренная блокировка. Запуск невозможен.",
	"need_onboarding":   "Сначала пройдите онбординг: /start.",
	"no_servers":        "Нет подключённых серверов.",
	"no_access_chat":    "У вас нет доступа к серверам этого чата.",
	"server_not_found":  "Сервер не найден.",
	"chat_not_linked":   "Чат не привязан к владельцу.",
	"not_set":           "Не указан",
	"unknown":           "Неизвестно",
	"none_short":        "нет",
	"on":                "вкл",
	"off":               "выкл",
	"lockdown_short_on": "🔒 вкл",
	"err_prefix":        "❌ %s",
	"no_access_hint":    "У вас нет доступа. Нажмите «🙋 Запросить доступ».",

	// --- aternos ---
	"err_cloudflare":        "⚠️ Aternos временно заблокировал запрос (Cloudflare). Подождите 3-5 минут или обновите куку /set_session.",
	"err_session_expired":   "⚠️ Сессия Aternos истекла или недействительна!\nОбновите куку командой /set_session в личке с ботом.",
	"err_owner_not_found":   "Владелец не найден: пройдите онбординг заново.",
	"err_session_not_set":   "Сессия Aternos не настроена: выполните /set_session.",
	"err_server_not_found":  "Сервер не найден или не принадлежит владельцу.",
	"err_profile_read":      "Не удалось прочитать профиль: %v",
	"err_request_failed":    "Не удалось выполнить запрос к Aternos",
	"srv_start_requested":   "Запуск сервера %s запрошен.",
	"srv_stop_requested":    "Остановка сервера %s запрошена.",
	"sess_expired_notify":   "⚠️ <b>Внимание!</b> Сессия Aternos истекла. Скопируйте новую куку ATERNOS_SESSION и отправьте:\n<code>/set_session ВАША_КУКА</code>",

	// --- панель владельца ---
	"panel_title":          "🛠 <b>Панель владельца</b>\n\n👤 %s\n🖥 Серверы: %d\n💬 Чаты: %d\n🔒 Локдаун: %s\n\nВыберите раздел:",
	"no_access_owner":      "Нет доступа: вы не владелец.",
	"panel_servers":        "<b>Ваши серверы:</b>\n",
	"panel_servers_empty":  "<b>Ваши серверы:</b>\nНет серверов. Пройдите онбординг заново: /start.",
	"panel_chats":          "<b>Ваши чаты:</b>\n",
	"panel_chats_empty":    "<b>Ваши чаты:</b>\nНет чатов. Добавьте бота в группу и напишите /link.",
	"settings_title":       "⚙️ <b>Настройки</b> (применяются ко всем серверам):",
	"audit_empty":          "📋 <b>Аудит:</b>\nПока нет записей.",
	"audit_title":          "📋 <b>Аудит:</b>",
	"send_new_cookie":      "🔑 Отправь новую куку <code>ATERNOS_SESSION</code> — сообщение будет сразу удалено.",
	"delete_account_title": "🗑 <b>Удалить аккаунт?</b>\n\nБудут удалены все серверы и отвязаны все чаты. Онбординг можно будет пройти заново через /start.",
	"servers_added":        "➕ Добавлены: %s",
	"servers_reactivated":  "↩️ Возвращены: %s",
	"servers_removed":      "➖ Отключены: %s",
	"state_online":         "🟢 ОНЛАЙН",
	"state_offline":        "🔴 ОФФЛАЙН",
	"players_inline":       " · %d/%d игроков",
	"server_card":          "🖥 <b>%s</b>\n🌐 IP: <code>%s</code>\n%s%s\n✅ Автоподтверждение: %s\n\n<i>Настройка автоподтверждения — в разделе «Настройки».</i>",
	"confirm_ok":           "✅ Запуск подтверждён.",
	"server_disabled":      "Сервер отключён.",
	"chat_unlinked":        "Чат отвязан.",
	"chat_not_yours":       "Чат не найден или не принадлежит вам.",
	"users_title":          "👥 <b>Участники чата</b> (всего: %d)",
	"no_users":             "Пока никто не зарегистрирован.",
	"chat_card":            "💬 <b>%s</b>\nID: <code>%d</code>\n🔗 Серверы в группе: %s\n\nНажмите на сервер, чтобы подключить или отключить его в этой группе:",
	"bind_ok":              "✅ %s подключён к группе.",
	"unbind_ok":            "🚫 %s отключён от группы.",
	"lockdown_off_ok":      "✅ Локдаун снят.",
	"lockdown_on_ok":       "🔒 Локдаун включён: управление серверами приостановлено.",
	"auto_confirm_all":     "Автоподтверждение: %s (все серверы)",
	"set_session_usage":    "Использование: /set_session <кука ATERNOS_SESSION>",
	"cookie_updated":       "✅ Кука обновлена! Кеш клиента сброшен.",
	"cookie_invalid":       "❌ Кука недействительна: %s",
	"announce_usage":       "Использование: /announce <текст>",
	"announce_sent":        "Рассылка отправлена %d/%d владельцам.",
	"account_deleted":      "🗑 <b>Аккаунт удалён.</b>\nНапишите /start, чтобы пройти онбординг заново.",
	"emergency_text":       "🚨 <b>Экстренная блокировка включена.</b>\nПрава всех пользователей отозваны (has_access=0). Запуск серверов невозможен, пока локдаун не будет снят в /panel → ⚙️ Настройки → «Локдаун».",
	"help_owner": "<b>Команды владельца:</b>\n" +
		"/panel — панель (серверы, чаты, настройки, аудит)\n" +
		"/run — запустить все серверы\n" +
		"/confirm — подтвердить очередь запуска\n" +
		"/status — статус всех серверов\n" +
		"/players — игроки онлайн\n" +
		"/info — карточка сервера\n" +
		"/stats — статистика запусков\n" +
		"/schedule 18:00 — расписание автозапуска\n" +
		"/set_session — обновить куку Aternos\n" +
		"/emergency — экстренный локдаун (права всем: OFF)\n\n" +
		"<b>В группе:</b>\n" +
		"/link — привязать чат (серверы выбираются в ЛС)\n" +
		"/unlink — отвязать чат\n" +
		"/run — запустить серверы чата\n" +
		"/confirm — подтвердить очередь\n" +
		"/status — статус серверов чата\n" +
		"/players — игроки онлайн\n" +
		"/info — карточка сервера\n" +
		"/grant @username — выдать доступ (владелец)\n" +
		"/revoke @username — забрать доступ (владелец)",

	// --- группа ---
	"link_already_other":    "❌ Этот чат уже привязан к другому владельцу.",
	"link_ok":               "✅ Чат привязан!\n\nТеперь выберите, какие серверы будут доступны в этой группе:\n📱 ЛС с ботом → /panel → 💬 Чаты → «🔗 Серверы чата».\n\nДашборд появится, как только вы подключите первый сервер.",
	"chat_not_linked_reply": "Чат не привязан.",
	"unlink_owner_only":     "❌ Только владелец чата может отвязать его.",
	"unlink_ok":             "✅ Чат отвязан. Дашборд остановлен.",
	"run_pick":              "▶️ <b>Какой сервер запустить?</b>",
	"run_started":           "🚀 <b>Запуск запрошен:</b> %s",
	"run_skipped":           "⏳ <b>Пропущены:</b> %s",
	"run_none":              "Ничего не запущено.",
	"players_title":         "👥 Игроки (%d/%d):",
	"players_none":          "На серверах нет игроков.",
	"access_usage":          "Использование: %s @username или %s user_id",
	"user_not_found":        "❌ Пользователь @%s не найден. Пусть он напишет сообщение в этот чат (для регистрации), или укажите user_id напрямую: %s <code>123456789</code>",
	"access_granted":        "✅ %s получил доступ к серверам.",
	"access_revoked":        "🚫 %s больше не может управлять серверами.",
	"confirm_ok_list":       "✅ <b>Запуск подтверждён:</b> %s",
	"confirm_skipped_list":  "⏳ <b>Не подтверждены (нет очереди/ошибка):</b> %s",
	"confirm_none":          "Ничего не подтверждено.",
	"dash_updated":          "🔄 Дашборд обновлён.",
	"you_are_owner":         "Вы — владелец этого чата 😉",
	"already_has_access":    "У вас уже есть доступ.",
	"access_cooldown":       "Запрос уже отправлен — подождите 5 минут.",
	"access_req_title":      "🙋 <b>Запрос доступа</b>\nЧат: %s (%d)\nПользователь: %s (@<code>%s</code>)",
	"access_req_fail":       "Не удалось отправить запрос владельцу.",
	"access_req_sent":       "Запрос отправлен владельцу.",
	"access_only_owner":     "Только владелец чата может решать этот запрос.",
	"access_approved_edit":  "\n\n✅ <b>Доступ одобрен.</b>",
	"access_denied_edit":    "\n\n❌ <b>Отклонено.</b>",
	"access_approved_pm":    "✅ Вам выдан доступ к серверам чата «%s».",
	"access_approved_chat":  "✅ Доступ одобрен. Пользователь может управлять серверами чата.",
	"btn_works_group":       "Кнопки дашборда работают в групповом чате.",
	"friendly_already":      "⏳ Сервер уже запускается, ожидайте.",
	"friendly_cloudflare":   "⚠️ Aternos временно ограничивает запросы, попробуйте позже.",
	"friendly_session":      "⚠️ Сессия Aternos истекла, сообщите владельцу.",
	"friendly_unavailable":  "❌ Сервер недоступен, ожидайте.",
	"help_group_owner": "<b>Команды владельца чата:</b>\n" +
		"/link — привязать чат (серверы выбираются в ЛС)\n" +
		"/unlink — отвязать чат\n" +
		"/status — статус серверов чата\n" +
		"/players — игроки онлайн\n" +
		"/info — карточка сервера\n" +
		"/run — запустить серверы чата\n" +
		"/grant @username — выдать доступ\n" +
		"/revoke @username — забрать доступ\n" +
		"/emergency — локдаун (в ЛС с ботом)\n\n" +
		"<b>Кнопки дашборда:</b>\n" +
		"▶️ Старт · ⏹ Стоп · ✅ Подтвердить (появляется, когда сервер в очереди)\n" +
		"Также доступно: /confirm — подтвердить очередь вручную",
	"help_group_user": "<b>Доступные действия:</b>\n" +
		"/status — статус серверов чата\n" +
		"/players — игроки онлайн\n" +
		"/info — карточка сервера\n" +
		"/run — запустить серверы чата\n" +
		"🙋 Запросить доступ — кнопка под дашбордом\n\n" +
		"Управление серверами доступно владельцу и одобренным участникам.",

	// --- кнопки ---
	"btn_servers":        "🖥 Серверы",
	"btn_chats":          "💬 Чаты",
	"btn_settings":       "⚙️ Настройки",
	"btn_audit":          "📋 Аудит",
	"btn_refresh_servers": "🔄 Обновить серверы",
	"btn_refresh_cookie": "🔄 Обновить куку",
	"btn_delete_account": "🗑 Удалить аккаунт",
	"btn_no_servers":     "(серверов нет)",
	"btn_back":           "🔙 Назад",
	"btn_stop":           "⏹ Стоп",
	"btn_start":          "▶️ Старт",
	"btn_confirm":        "✅ Подтвердить",
	"btn_delete":         "🗑 Удалить",
	"btn_to_servers":     "🔙 К серверам",
	"btn_no_chats":       "(чатов нет)",
	"btn_users":          "👥 Участники",
	"btn_unlink_chat":    "🗑 Отвязать чат",
	"btn_to_chats":       "🔙 К чатам",
	"btn_prev":           "⬅️ Назад",
	"btn_next":           "➡️ Вперёд",
	"btn_to_chat":        "🔙 К чату",
	"btn_ac":             "✅ Автоподтверждение",
	"btn_ld_off":         "🔓 Локдаун выкл",
	"btn_ld_on":          "🔒 Локдаун вкл",
	"btn_yes_delete":     "🗑 Да, удалить всё",
	"btn_cancel":         "↩️ Отмена",
	"btn_refresh":        "🔄 Обновить",
	"btn_request_access": "🙋 Запросить доступ",
	"btn_approve":        "✅ Одобрить",
	"btn_deny":           "❌ Отклонить",
	"btn_done":           "Готово (%d)",
	"btn_lang":           "🌐 Язык: %s",
	"lang_switched":      "✅ Язык интерфейса: %s",

	// --- дашборд ---
	"dash_no_servers":     "🔴 <b>В чате нет подключённых серверов.</b>\nВладелец: /link\n\n%s",
	"dash_pm_no_servers":  "🔴 <b>Нет подключённых серверов.</b>\nОткройте /panel и нажмите «🔄 Обновить серверы».",
	"dash_status_online":  "🟢 <b>%s — ОНЛАЙН</b>",
	"dash_status_queue":   "🟡 <b>%s — В ОЧЕРЕДИ (позиция %d)</b> ⏳\n<i>(Ожидание запуска на Aternos...)</i>",
	"dash_status_starting": "🟡 <b>%s — ЗАПУСКАЕТСЯ / В ОЧЕРЕДИ...</b> ⏳\n<i>(Открытие портов Aternos занимает ~2-5 мин)</i>",
	"dash_status_offline": "🔴 <b>%s — ОФФЛАЙН</b>",
	"dash_ip":             "🌐 IP: <code>%s</code>",
	"dash_players_list":   "👥 Игроки (%d/%d):\n%s",
	"dash_players_num":    "👥 Игроки: %d/%d",
	"dash_version":        "📦 Версия: %s",
	"dash_timestamp":      "🕐 Обновлено: %s %s",
	"offline_notif":       "🔴 <b>%s</b> ушёл в оффлайн.",
	"srv_offline_notify":  "🔴 <b>%s</b> ушёл в оффлайн.",
	"players_join_many":   "🟢 <b>%s</b> зашли на %s",
	"players_leave_many":  "🔴 <b>%s</b> вышли с %s",
	"pin_help":            "⚠️ <b>Дайте боту права администратора в этой группе</b>, чтобы он мог закреплять дашборд.\nНастройки группы → Управление группами → администраторы → добавить бота.",
	"cookie_expired_pm":   "⚠️ <b>Кука Aternos просрочена или недействительна!</b>\n\nОбновите её: /set_session или кнопка «🔄 Обновить куку» в панели /panel.",

	// --- queuewatcher ---
	"qw_online":        "🟢 <b>%s</b> запущен и готов к игре!\n🌐 IP: <code>%s</code>",
	"qw_failed":        "⚠️ <b>Автоподтверждение запуска не сработало:</b>\n🖥 %s\n%s\nПроверьте Aternos вручную.",
	"qw_reason_idle":   "сервер не запустился (20 минут вне очереди)",
	"qw_reason_poll":   "опрос очереди не удался: %v",
	"qw_join":          "🟢 <b>%s</b> зашёл на %s",
	"qw_leave":         "🔴 <b>%s</b> вышел с %s",

	// --- онбординг ---
	"onb_welcome": "👋 <b>Привет! Я MineOps — бот для управления серверами Aternos.</b>\n\n" +
		"Отправьте куку <code>ATERNOS_SESSION</code> — я проверю её, и вы сможете " +
		"управлять своими серверами прямо в Telegram.\n\n" +
		"💡 <b>Как получить куку:</b> зайдите на aternos.org → откройте консоль " +
		"браузера (F12) → вкладка Network → обновите страницу → найдите запрос " +
		"на ваш аккаунт → закладка Cookies → скопируйте значение " +
		"<code>ATERNOS_SESSION</code>.\n\n" +
		"<i>🔒 Кука шифруется (Fernet) и хранится только в вашем профиле бота. " +
		"Сообщение с кукой сразу удаляется.</i>",
	"onb_checking":        "⏳ Проверяю сессию Aternos и ищу серверы...",
	"onb_no_servers":      "На аккаунте Aternos не найдено серверов. Проверьте аккаунт и попробуйте снова.",
	"onb_valid":           "✅ Сессия действительна! Выберите серверы для управления:",
	"onb_session_expired": "Сессия устарела — начните заново /start.",
	"onb_select_any":      "Выберите хотя бы один сервер.",
	"onb_saving":          "💾 Сохраняю аккаунт...",
	"onb_create_fail":     "❌ Не удалось создать аккаунт. Попробуйте /start заново.",
	"onb_done": "🎉 <b>Готово!</b> Ваш аккаунт создан.\n\n" +
		"1️⃣ Добавьте бота в свою группу и выдайте ему права администратора;\n" +
		"2️⃣ В группе напишите <b>/link</b>;\n" +
		"3️⃣ В ЛС: /panel → 💬 Чаты → подключите нужные серверы для группы;\n" +
		"4️⃣ Дашборд со статусами закрепится в чате автоматически.\n\n" +
		"Подключено серверов: <b>%s</b>\n\n" +
		"Команды: /panel — панель владельца, /set_session — обновить куку.",

	// --- новые команды ---
	"ping":           "🏓 <b>Понг!</b>\n⏱️ Задержка: %d мс",
	"stats_title":    "📊 <b>Статистика запусков</b>",
	"stats_empty":    "Пока нет данных о запусках.",
	"stats_total":    "🖥 Всего запусков: %d",
	"stats_7d":       "📅 За последние 7 дней: %d",
	"stats_30d":      "📅 За последние 30 дней: %d",
	"stats_recent_starts": "\n\n👤 <b>Последние запуски:</b>",
	"time_ago_just_now":   "только что",
	"time_ago_min":        "%dм назад",
	"time_ago_hour":       "%dч назад",
	"time_ago_yesterday":  "вчера",
	"time_ago_days":       "%dд назад",
	"stats_launch":   "🖥 %s — %d запусков",
	"stats_recent":   "\n\n📋 <b>Последние действия:</b>",
	"sched_usage":    "Использование: /schedule 18:00 — ежедневно; /schedule 18:00 once — один раз; /schedule off — отключить.\nТекущее: %s",
	"sched_off":      "🚫 Автозапуск отключён.",
	"sched_bad_time": "❌ Неверное время. Формат: HH:MM (например 18:00).",
	"sched_on":       "⏰ Автозапуск включён: каждый день в %s.",
	"sched_on_once":  "⏰ Автозапуск включён: один раз в %s.",
	"sched_fired":    "⏰ <b>Плановый запуск</b>\nЗапущены: %s",

	// --- названия действий (для /stats) ---
	"act_server_start":        "запуск",
	"act_server_stop":         "остановка",
	"act_server_confirm":      "подтверждение запуска",
	"act_servers_refresh":     "обновление серверов",
	"act_chat_link":           "привязка чата",
	"act_chat_unlink":         "отвязка чата",
	"act_chat_server_bind":    "сервер в группе",
	"act_chat_server_unbind":  "сервер из группы",
	"act_access_grant":        "доступ выдан",
	"act_access_deny":         "заявка отклонена",
	"act_access_request":      "запрос доступа",
	"act_access_revoke":       "доступ отозван",
	"act_emergency":           "экстренный локдаун",
	"act_lockdown":            "локдаун",
	"act_auto_confirm":        "автоподтверждение",
	"act_server_deactivate":   "сервер отключён",
	"act_onboarding":          "онбординг",
	"act_session_update":      "обновление куки",
	"act_session_expired":     "просрочка куки",
	"act_auto_confirm_failed": "автоподтверждение не сработало",

	// --- описания команд (Telegram /setCommands) ---
	"cmd_start":      "Панель владельца / онбординг",
	"cmd_panel":      "Панель владельца",
	"cmd_help":       "Справка",
	"cmd_status":     "Статус серверов (группа / ЛС)",
	"cmd_run":        "Запустить серверы (группа / ЛС)",
	"cmd_players":    "Игроки онлайн (группа / ЛС)",
	"cmd_confirm":    "Подтвердить очередь запуска",
	"cmd_link":       "Привязать чат (в группе, владелец)",
	"cmd_unlink":     "Отвязать чат (в группе, владелец)",
	"cmd_grant":      "Выдать доступ в чате (владелец)",
	"cmd_revoke":     "Забрать доступ в чате (владелец)",
	"cmd_set_session": "Обновить куку Aternos",
	"cmd_emergency":  "Локдаун вкл/выкл",
	"cmd_ping":       "Проверка задержки",
	"cmd_info":       "Карточка сервера",
	"cmd_stats":      "Статистика запусков",
	"cmd_schedule":   "Расписание автозапуска",
}

var en = map[string]string{
	// --- common ---
	"lockdown_blocked":  "🚨 Emergency lockdown is active. Starting is impossible.",
	"need_onboarding":   "Please complete onboarding first: /start.",
	"no_servers":        "No connected servers.",
	"no_access_chat":    "You don't have access to this chat's servers.",
	"server_not_found":  "Server not found.",
	"chat_not_linked":   "Chat is not linked to an owner.",
	"not_set":           "Not set",
	"unknown":           "Unknown",
	"none_short":        "none",
	"on":                "on",
	"off":               "off",
	"lockdown_short_on": "🔒 on",
	"err_prefix":        "❌ %s",
	"no_access_hint":    "You don't have access. Tap “🙋 Request access”.",

	// --- aternos ---
	"err_cloudflare":       "⚠️ Aternos temporarily blocked the request (Cloudflare). Wait 3-5 minutes or refresh your cookie with /set_session.",
	"err_session_expired":  "⚠️ Aternos session expired or invalid!\nRefresh your cookie with /set_session in a private chat with the bot.",
	"err_owner_not_found":  "Owner not found: please complete onboarding again.",
	"err_session_not_set":  "Aternos session is not set: run /set_session.",
	"err_server_not_found": "Server not found or doesn't belong to the owner.",
	"err_profile_read":     "Failed to read profile: %v",
	"err_request_failed":   "Failed to make a request to Aternos",
	"srv_start_requested":  "Start requested for server %s.",
	"srv_stop_requested":   "Stop requested for server %s.",
	"sess_expired_notify":  "⚠️ <b>Attention!</b> Your Aternos session expired. Copy the new ATERNOS_SESSION cookie and send:\n<code>/set_session YOUR_COOKIE</code>",

	// --- owner panel ---
	"panel_title":          "🛠 <b>Owner panel</b>\n\n👤 %s\n🖥 Servers: %d\n💬 Chats: %d\n🔒 Lockdown: %s\n\nSelect a section:",
	"no_access_owner":      "No access: you are not an owner.",
	"panel_servers":        "<b>Your servers:</b>\n",
	"panel_servers_empty":  "<b>Your servers:</b>\nNo servers. Please complete onboarding again: /start.",
	"panel_chats":          "<b>Your chats:</b>\n",
	"panel_chats_empty":    "<b>Your chats:</b>\nNo chats. Add the bot to a group and send /link.",
	"settings_title":       "⚙️ <b>Settings</b> (applied to all servers):",
	"audit_empty":          "📋 <b>Audit:</b>\nNo records yet.",
	"audit_title":          "📋 <b>Audit:</b>",
	"send_new_cookie":      "🔑 Send the new <code>ATERNOS_SESSION</code> cookie — the message will be deleted immediately.",
	"delete_account_title": "🗑 <b>Delete account?</b>\n\nAll servers will be removed and all chats unlinked. You can complete onboarding again with /start.",
	"servers_added":        "➕ Added: %s",
	"servers_reactivated":  "↩️ Reactivated: %s",
	"servers_removed":      "➖ Disabled: %s",
	"state_online":         "🟢 ONLINE",
	"state_offline":        "🔴 OFFLINE",
	"players_inline":       " · %d/%d players",
	"server_card":          "🖥 <b>%s</b>\n🌐 IP: <code>%s</code>\n%s%s\n✅ Auto-confirm: %s\n\n<i>Auto-confirm is configured in “Settings”.</i>",
	"confirm_ok":           "✅ Start confirmed.",
	"server_disabled":      "Server disabled.",
	"chat_unlinked":        "Chat unlinked.",
	"chat_not_yours":       "Chat not found or doesn't belong to you.",
	"users_title":          "👥 <b>Chat members</b> (total: %d)",
	"no_users":             "No members registered yet.",
	"chat_card":            "💬 <b>%s</b>\nID: <code>%d</code>\n🔗 Servers in group: %s\n\nTap a server to bind or unbind it in this group:",
	"bind_ok":              "✅ %s is now in this group.",
	"unbind_ok":            "🚫 %s was removed from this group.",
	"lockdown_off_ok":      "✅ Lockdown lifted.",
	"lockdown_on_ok":       "🔒 Lockdown enabled: server management is paused.",
	"auto_confirm_all":     "Auto-confirm: %s (all servers)",
	"set_session_usage":    "Usage: /set_session <ATERNOS_SESSION cookie>",
	"cookie_updated":       "✅ Cookie updated! Client cache cleared.",
	"cookie_invalid":       "❌ Invalid cookie: %s",
	"announce_usage":       "Usage: /announce <text>",
	"announce_sent":        "Broadcast sent to %d/%d owners.",
	"account_deleted":      "🗑 <b>Account deleted.</b>\nSend /start to complete onboarding again.",
	"emergency_text":       "🚨 <b>Emergency lockdown enabled.</b>\nAll users' rights revoked (has_access=0). Starting servers is impossible until lockdown is lifted in /panel → ⚙️ Settings → “Lockdown”.",
	"help_owner": "<b>Owner commands:</b>\n" +
		"/panel — panel (servers, chats, settings, audit)\n" +
		"/run — start all servers\n" +
		"/confirm — confirm the start queue\n" +
		"/status — status of all servers\n" +
		"/players — online players\n" +
		"/info — server info card\n" +
		"/stats — launch statistics\n" +
		"/schedule 18:00 — auto-start schedule\n" +
		"/set_session — refresh your Aternos cookie\n" +
		"/emergency — emergency lockdown (all rights: OFF)\n\n" +
		"<b>In a group:</b>\n" +
		"/link — link the chat (servers are picked in DM)\n" +
		"/unlink — unlink the chat\n" +
		"/run — start the chat's servers\n" +
		"/confirm — confirm the queue\n" +
		"/status — status of the chat's servers\n" +
		"/players — online players\n" +
		"/info — server info card\n" +
		"/grant @username — grant access (owner)\n" +
		"/revoke @username — revoke access (owner)",

	// --- group ---
	"link_already_other":    "❌ This chat is already linked to another owner.",
	"link_ok":               "✅ Chat linked!\n\nNow pick which servers will be available in this group:\n📱 DM with the bot → /panel → 💬 Chats → “🔗 Chat servers”.\n\nThe dashboard will appear once you link the first server.",
	"chat_not_linked_reply": "Chat is not linked.",
	"unlink_owner_only":     "❌ Only the chat owner can unlink it.",
	"unlink_ok":             "✅ Chat unlinked. Dashboard stopped.",
	"run_pick":              "▶️ <b>Which server to start?</b>",
	"run_started":           "🚀 <b>Start requested:</b> %s",
	"run_skipped":           "⏳ <b>Skipped:</b> %s",
	"run_none":              "Nothing started.",
	"players_title":         "👥 Players (%d/%d):",
	"players_none":          "No players online.",
	"access_usage":          "Usage: %s @username or %s user_id",
	"user_not_found":        "❌ User @%s not found. Ask them to send a message in this chat (to register), or provide user_id directly: %s <code>123456789</code>",
	"access_granted":        "✅ %s got access to the servers.",
	"access_revoked":        "🚫 %s can no longer manage the servers.",
	"confirm_ok_list":       "✅ <b>Start confirmed:</b> %s",
	"confirm_skipped_list":  "⏳ <b>Not confirmed (no queue/error):</b> %s",
	"confirm_none":          "Nothing confirmed.",
	"dash_updated":          "🔄 Dashboard updated.",
	"you_are_owner":         "You are the owner of this chat 😉",
	"already_has_access":    "You already have access.",
	"access_cooldown":       "Request already sent — wait 5 minutes.",
	"access_req_title":      "🙋 <b>Access request</b>\nChat: %s (%d)\nUser: %s (@<code>%s</code>)",
	"access_req_fail":       "Could not send the request to the owner.",
	"access_req_sent":       "Request sent to the owner.",
	"access_only_owner":     "Only the chat owner can decide on this request.",
	"access_approved_edit":  "\n\n✅ <b>Access approved.</b>",
	"access_denied_edit":    "\n\n❌ <b>Denied.</b>",
	"access_approved_pm":    "✅ You have been granted access to the servers of “%s”.",
	"access_approved_chat":  "✅ Access approved. The user can now manage the chat's servers.",
	"btn_works_group":       "Dashboard buttons work in group chats.",
	"friendly_already":      "⏳ Server is already starting, please wait.",
	"friendly_cloudflare":   "⚠️ Aternos is temporarily rate-limiting requests, try later.",
	"friendly_session":      "⚠️ Aternos session expired, notify the owner.",
	"friendly_unavailable":  "❌ Server unavailable, please wait.",
	"help_group_owner": "<b>Chat owner commands:</b>\n" +
		"/link — link the chat (servers are picked in DM)\n" +
		"/unlink — unlink the chat\n" +
		"/status — status of the chat's servers\n" +
		"/players — online players\n" +
		"/info — server info card\n" +
		"/run — start the chat's servers\n" +
		"/grant @username — grant access\n" +
		"/revoke @username — revoke access\n" +
		"/emergency — lockdown (in DM with the bot)\n\n" +
		"<b>Dashboard buttons:</b>\n" +
		"▶️ Start · ⏹ Stop · ✅ Confirm (appears when the server is in queue)\n" +
		"Also available: /confirm — confirm the queue manually",
	"help_group_user": "<b>Available actions:</b>\n" +
		"/status — status of the chat's servers\n" +
		"/players — online players\n" +
		"/info — server info card\n" +
		"/run — start the chat's servers\n" +
		"🙋 Request access — button under the dashboard\n\n" +
		"Server management is available to the owner and approved members.",

	// --- buttons ---
	"btn_servers":         "🖥 Servers",
	"btn_chats":           "💬 Chats",
	"btn_settings":        "⚙️ Settings",
	"btn_audit":           "📋 Audit",
	"btn_refresh_servers": "🔄 Refresh servers",
	"btn_refresh_cookie":  "🔄 Refresh cookie",
	"btn_delete_account":  "🗑 Delete account",
	"btn_no_servers":      "(no servers)",
	"btn_back":            "🔙 Back",
	"btn_stop":            "⏹ Stop",
	"btn_start":           "▶️ Start",
	"btn_confirm":         "✅ Confirm",
	"btn_delete":          "🗑 Delete",
	"btn_to_servers":      "🔙 To servers",
	"btn_no_chats":        "(no chats)",
	"btn_users":           "👥 Members",
	"btn_unlink_chat":     "🗑 Unlink chat",
	"btn_to_chats":        "🔙 To chats",
	"btn_prev":            "⬅️ Back",
	"btn_next":            "➡️ Next",
	"btn_to_chat":         "🔙 To chat",
	"btn_ac":              "✅ Auto-confirm",
	"btn_ld_off":          "🔓 Lockdown off",
	"btn_ld_on":           "🔒 Lockdown on",
	"btn_yes_delete":      "🗑 Yes, delete everything",
	"btn_cancel":          "↩️ Cancel",
	"btn_refresh":         "🔄 Refresh",
	"btn_request_access":  "🙋 Request access",
	"btn_approve":         "✅ Approve",
	"btn_deny":            "❌ Deny",
	"btn_done":            "Done (%d)",
	"btn_lang":            "🌐 Language: %s",
	"lang_switched":       "✅ Interface language: %s",

	// --- dashboard ---
	"dash_no_servers":      "🔴 <b>No servers connected in this chat.</b>\nOwner: /link\n\n%s",
	"dash_pm_no_servers":   "🔴 <b>No servers connected.</b>\nOpen /panel and tap “🔄 Refresh servers”.",
	"dash_status_online":   "🟢 <b>%s — ONLINE</b>",
	"dash_status_queue":    "🟡 <b>%s — IN QUEUE (position %d)</b> ⏳\n<i>(Waiting for Aternos to start...)</i>",
	"dash_status_starting": "🟡 <b>%s — STARTING / IN QUEUE...</b> ⏳\n<i>(Aternos port opening takes ~2-5 min)</i>",
	"dash_status_offline":  "🔴 <b>%s — OFFLINE</b>",
	"dash_ip":              "🌐 IP: <code>%s</code>",
	"dash_players_list":    "👥 Players (%d/%d):\n%s",
	"dash_players_num":     "👥 Players: %d/%d",
	"dash_version":         "📦 Version: %s",
	"dash_timestamp":        "🕐 Updated: %s %s",
"offline_notif":      "🔴 <b>%s</b> went offline.",
	"srv_offline_notify": "🔴 <b>%s</b> went offline.",
	"players_join_many":  "🟢 <b>%s</b> joined %s",
	"players_leave_many": "🔴 <b>%s</b> left %s",
	"pin_help":             "⚠️ <b>Give the bot admin rights in this group</b> so it can pin the dashboard.\nGroup settings → Group management → administrators → add the bot.",
	"cookie_expired_pm":    "⚠️ <b>Aternos cookie expired or invalid!</b>\n\nRefresh it: /set_session or the “🔄 Refresh cookie” button in /panel.",

	// --- queuewatcher ---
	"qw_online":       "🟢 <b>%s</b> is up and ready to play!\n🌐 IP: <code>%s</code>",
	"qw_failed":       "⚠️ <b>Auto-confirm did not work:</b>\n🖥 %s\n%s\nCheck Aternos manually.",
	"qw_reason_idle":  "server did not start (20 minutes out of queue)",
	"qw_reason_poll":  "queue poll failed: %v",
	"qw_join":         "🟢 <b>%s</b> joined %s",
	"qw_leave":        "🔴 <b>%s</b> left %s",

	// --- onboarding ---
	"onb_welcome": "👋 <b>Hi! I'm MineOps — a bot for managing Aternos servers.</b>\n\n" +
		"Send the <code>ATERNOS_SESSION</code> cookie — I'll verify it and you'll be able " +
		"to manage your servers right from Telegram.\n\n" +
		"💡 <b>How to get the cookie:</b> go to aternos.org → open the browser console " +
		"(F12) → Network tab → refresh the page → find a request to your account → " +
		"Cookies tab → copy the <code>ATERNOS_SESSION</code> value.\n\n" +
		"<i>🔒 The cookie is encrypted (Fernet) and stored only in your bot profile. " +
		"The message with the cookie is deleted immediately.</i>",
	"onb_checking":        "⏳ Checking Aternos session and looking for servers...",
	"onb_no_servers":      "No servers found on the Aternos account. Check the account and try again.",
	"onb_valid":           "✅ Session is valid! Select servers to manage:",
	"onb_session_expired": "Session expired — start over with /start.",
	"onb_select_any":      "Select at least one server.",
	"onb_saving":          "💾 Saving account...",
	"onb_create_fail":     "❌ Could not create account. Try /start again.",
	"onb_done": "🎉 <b>Done!</b> Your account has been created.\n\n" +
		"1️⃣ Add the bot to your group and give it admin rights;\n" +
		"2️⃣ In the group send <b>/link</b>;\n" +
		"3️⃣ In DM: /panel → 💬 Chats → link the needed servers for the group;\n" +
		"4️⃣ The status dashboard will be pinned in the chat automatically.\n\n" +
		"Servers connected: <b>%s</b>\n\n" +
		"Commands: /panel — owner panel, /set_session — refresh the cookie.",

	// --- new commands ---
	"ping":           "🏓 <b>Pong!</b>\n⏱️ Latency: %d ms",
	"stats_title":    "📊 <b>Launch statistics</b>",
	"stats_empty":    "No launch data yet.",
	"stats_total":    "🖥 Total launches: %d",
	"stats_7d":       "📅 Last 7 days: %d",
	"stats_30d":      "📅 Last 30 days: %d",
	"stats_recent_starts": "\n\n👤 <b>Recent launches:</b>",
	"time_ago_just_now":   "just now",
	"time_ago_min":        "%dm ago",
	"time_ago_hour":       "%dh ago",
	"time_ago_yesterday":  "yesterday",
	"time_ago_days":       "%dd ago",
	"stats_launch":   "🖥 %s — %d launches",
	"stats_recent":   "\n\n📋 <b>Recent activity:</b>",
	"sched_usage":    "Usage: /schedule 18:00 — daily; /schedule 18:00 once — one time; /schedule off — disable.\nCurrent: %s",
	"sched_off":      "🚫 Auto-start disabled.",
	"sched_bad_time": "❌ Invalid time. Format: HH:MM (e.g. 18:00).",
	"sched_on":       "⏰ Auto-start enabled: daily at %s.",
	"sched_on_once":  "⏰ Auto-start enabled: once at %s.",
	"sched_fired":    "⏰ <b>Scheduled start</b>\nStarted: %s",

	// --- action names (for /stats) ---
	"act_server_start":        "start",
	"act_server_stop":         "stop",
	"act_server_confirm":      "start confirmation",
	"act_servers_refresh":     "server refresh",
	"act_chat_link":           "chat linked",
	"act_chat_unlink":         "chat unlinked",
	"act_chat_server_bind":    "server in group",
	"act_chat_server_unbind":  "server removed from group",
	"act_access_grant":        "access granted",
	"act_access_deny":         "request denied",
	"act_access_request":      "access request",
	"act_access_revoke":       "access revoked",
	"act_emergency":           "emergency lockdown",
	"act_lockdown":            "lockdown",
	"act_auto_confirm":        "auto-confirm",
	"act_server_deactivate":   "server deactivated",
	"act_onboarding":          "onboarding",
	"act_session_update":      "session update",
	"act_session_expired":     "session expired",
	"act_auto_confirm_failed": "auto-confirm failed",

	// --- command descriptions ---
	"cmd_start":       "Owner panel / onboarding",
	"cmd_panel":       "Owner panel",
	"cmd_help":        "Help",
	"cmd_status":      "Server status (group / DM)",
	"cmd_run":         "Start servers (group / DM)",
	"cmd_players":     "Online players (group / DM)",
	"cmd_confirm":     "Confirm start queue",
	"cmd_link":        "Link chat (in group, owner)",
	"cmd_unlink":      "Unlink chat (in group, owner)",
	"cmd_grant":       "Grant access in chat (owner)",
	"cmd_revoke":      "Revoke access in chat (owner)",
	"cmd_set_session": "Refresh Aternos cookie",
	"cmd_emergency":   "Lockdown on/off",
	"cmd_ping":        "Check latency",
	"cmd_info":        "Server info card",
	"cmd_stats":       "Launch statistics",
	"cmd_schedule":    "Schedule auto-start",
}