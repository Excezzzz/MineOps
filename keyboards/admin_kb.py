"""Клавиатуры личной панели владельца (DM-чат с ботом)."""

from __future__ import annotations

from aiogram.filters.callback_data import CallbackData
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

# Панель владельца.
class PanelCb(CallbackData, prefix="panel"):
    action: str  # "servers" | "chats" | "settings" | "audit" | "back" | "add_server" | "refresh_servers" | "refresh_session" | "delete_account"

# Карточка сервера в панели.
class PanelServerCb(CallbackData, prefix="psrv"):
    server_id: int

# Действия над сервером из панели.
class PanelActionCb(CallbackData, prefix="pact"):
    server_id: int
    action: str  # "start" | "stop" | "delete" | "confirm"

# Карточка чата в панели.
class PanelChatCb(CallbackData, prefix="pchat"):
    chat_id: int
    action: str  # "select" | "unlink" | "users"

# Пагинация участников чата.
class UsersPageCb(CallbackData, prefix="upage"):
    chat_id: int
    page: int

# Настройки владельца.
class OwnerSettingsCb(CallbackData, prefix="oset"):
    action: str     # "auto_confirm" | "lockdown"
    enabled: bool   # целевое значение (вкл/выкл)

# Обновление серверов из аккаунта Aternos (чекбоксы как при онбординге).
class RefreshServerCb(CallbackData, prefix="rsrv"):
    action: str         # "toggle" | "done" | "cancel"
    aternos_id: str = ""

# Подтверждение удаления аккаунта.
class DeleteAccountCb(CallbackData, prefix="delacc"):
    confirm: bool


def get_owner_panel_kb() -> InlineKeyboardMarkup:
    """Главное меню панели владельца."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text="🖥 Серверы", callback_data=PanelCb(action="servers").pack())],
            [InlineKeyboardButton(text="💬 Чаты", callback_data=PanelCb(action="chats").pack())],
            [InlineKeyboardButton(text="⚙️ Настройки", callback_data=PanelCb(action="settings").pack())],
            [InlineKeyboardButton(text="📋 Аудит", callback_data=PanelCb(action="audit").pack())],
            [InlineKeyboardButton(text="🔄 Обновить серверы", callback_data=PanelCb(action="refresh_servers").pack())],
            [InlineKeyboardButton(text="🔄 Обновить куку", callback_data=PanelCb(action="refresh_session").pack())],
            [InlineKeyboardButton(text="🗑 Удалить аккаунт", callback_data=PanelCb(action="delete_account").pack())],
        ]
    )


def get_owner_servers_kb(servers: list[dict]) -> InlineKeyboardMarkup:
    """Список серверов владельца (кнопка = карточка сервера)."""
    buttons: list[list[InlineKeyboardButton]] = [
        [
            InlineKeyboardButton(
                text=f"🖥 {s['display_name']}",
                callback_data=PanelServerCb(server_id=s["id"]).pack(),
            )
        ]
        for s in servers
    ]
    if not servers:
        buttons.append([InlineKeyboardButton(text="(серверов нет)", callback_data="noop")])
    buttons.append([InlineKeyboardButton(text="🔙 Назад", callback_data=PanelCb(action="back").pack())])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_server_card_kb(server_id: int, is_online: bool) -> InlineKeyboardMarkup:
    """Карточка сервера: действия + удаление + возврат к списку."""
    action_row = []
    if is_online:
        action_row.append(
            InlineKeyboardButton(
                text="⏹ Стоп", callback_data=PanelActionCb(server_id=server_id, action="stop").pack()
            )
        )
    else:
        action_row.append(
            InlineKeyboardButton(
                text="▶️ Старт",
                callback_data=PanelActionCb(server_id=server_id, action="start").pack(),
            )
        )
        action_row.append(
            InlineKeyboardButton(
                text="✅ Подтвердить",
                callback_data=PanelActionCb(server_id=server_id, action="confirm").pack(),
            )
        )
    return InlineKeyboardMarkup(
        inline_keyboard=[
            action_row,
            [
                InlineKeyboardButton(
                    text="🗑 Удалить",
                    callback_data=PanelActionCb(server_id=server_id, action="delete").pack(),
                ),
                InlineKeyboardButton(
                    text="🔙 К серверам",
                    callback_data=PanelCb(action="servers").pack(),
                ),
            ],
        ]
    )


def get_owner_chats_kb(chats: list[dict]) -> InlineKeyboardMarkup:
    """Список чатов владельца (кнопка = карточка чата)."""
    buttons: list[list[InlineKeyboardButton]] = [
        [
            InlineKeyboardButton(
                text=f"💬 {chat['title'] or chat['chat_id']}",
                callback_data=PanelChatCb(chat_id=chat["chat_id"], action="select").pack(),
            )
        ]
        for chat in chats
    ]
    if not chats:
        buttons.append([InlineKeyboardButton(text="(чатов нет)", callback_data="noop")])
    buttons.append([InlineKeyboardButton(text="🔙 Назад", callback_data=PanelCb(action="back").pack())])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_chat_card_kb(chat_id: int) -> InlineKeyboardMarkup:
    """Карточка чата: участники, отвязка, возврат."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="👥 Участники",
                    callback_data=PanelChatCb(chat_id=chat_id, action="users").pack(),
                ),
                InlineKeyboardButton(
                    text="🔗 Серверы чата",
                    callback_data=PanelChatCb(chat_id=chat_id, action="select").pack(),
                ),
            ],
            [
                InlineKeyboardButton(
                    text="🗑 Отвязать чат",
                    callback_data=PanelChatCb(chat_id=chat_id, action="unlink").pack(),
                ),
                InlineKeyboardButton(
                    text="🔙 К чатам",
                    callback_data=PanelCb(action="chats").pack(),
                ),
            ],
        ]
    )


def get_users_page_kb(chat_id: int, page: int, total: int, limit: int) -> InlineKeyboardMarkup:
    """Пагинация участников чата (limit на страницу)."""
    pages = max((total + limit - 1) // limit, 1)
    nav: list[InlineKeyboardButton] = []
    if page > 0:
        nav.append(
            InlineKeyboardButton(
                text="⬅️ Назад", callback_data=UsersPageCb(chat_id=chat_id, page=page - 1).pack()
            )
        )
    nav.append(InlineKeyboardButton(text=f"{page + 1}/{pages}", callback_data="noop"))
    if page + 1 < pages:
        nav.append(
            InlineKeyboardButton(
                text="➡️ Вперёд", callback_data=UsersPageCb(chat_id=chat_id, page=page + 1).pack()
            )
        )
    return InlineKeyboardMarkup(
        inline_keyboard=[
            nav,
            [
                InlineKeyboardButton(
                    text="🔙 К чату",
                    callback_data=PanelChatCb(chat_id=chat_id, action="select").pack(),
                )
            ],
        ]
    )


def get_owner_settings_kb(lockdown: bool, auto_confirm: bool) -> InlineKeyboardMarkup:
    """Настройки владельца (применяются ко всем его серверам).

    Короткие кнопки-переключатели по одной на строку (текст не обрезается).
    """
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="✅ Автоподтверждение" if auto_confirm else "❌ Автоподтверждение",
                    callback_data=OwnerSettingsCb(action="auto_confirm", enabled=not auto_confirm).pack(),
                )
            ],
            [
                InlineKeyboardButton(
                    text="🔒 Локдаун вкл" if lockdown else "🔓 Локдаун выкл",
                    callback_data=OwnerSettingsCb(action="lockdown", enabled=not lockdown).pack(),
                )
            ],
            [InlineKeyboardButton(text="↩️ Назад", callback_data=PanelCb(action="back").pack())],
        ]
    )


def get_owner_audit_kb() -> InlineKeyboardMarkup:
    """Навигация журнала аудита."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text="🔙 Назад", callback_data=PanelCb(action="back").pack())]
        ]
    )


def get_refresh_servers_kb(
    servers: list[dict], selected: set[str]
) -> InlineKeyboardMarkup:
    """Список серверов аккаунта Aternos с чекбоксами (для обновления списка).

    Снятие галочки деактивирует сервер в БД, установка — (ре)активирует.
    """
    buttons: list[list[InlineKeyboardButton]] = []
    for s in servers:
        sid = str(s["aternos_id"])
        name = str(s.get("display_name") or f"ID {sid}")
        checked = "✅" if sid in selected else "⬜"
        buttons.append(
            [
                InlineKeyboardButton(
                    text=f"{checked} {name}",
                    callback_data=RefreshServerCb(action="toggle", aternos_id=sid).pack(),
                )
            ]
        )
    buttons.append(
        [
            InlineKeyboardButton(
                text="✅ Готово",
                callback_data=RefreshServerCb(action="done").pack(),
            ),
            InlineKeyboardButton(
                text="❌ Отмена",
                callback_data=RefreshServerCb(action="cancel").pack(),
            ),
        ]
    )
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_delete_account_kb() -> InlineKeyboardMarkup:
    """Подтверждение удаления аккаунта владельца."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="🗑 Да, удалить всё",
                    callback_data=DeleteAccountCb(confirm=True).pack(),
                ),
                InlineKeyboardButton(
                    text="↩️ Отмена",
                    callback_data=DeleteAccountCb(confirm=False).pack(),
                ),
            ]
        ]
    )
