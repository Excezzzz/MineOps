"""Клавиатуры чата: дашборд с кнопками управления и запрос доступа.

Multi-tenant: кнопки управления серверами привязаны к server_id владельца,
запросы доступа уходят владельцу конкретного чата (а не глобальному админу).
"""

from __future__ import annotations

from aiogram.filters.callback_data import CallbackData
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

# Кнопки управления сервером. server_id — id сервера в БД (владелец чата).
class ServerCb(CallbackData, prefix="srv"):
    server_id: int
    action: str  # "start" | "stop" | "confirm" | "status"

# Запрос доступа к серверу/чату.
class ReqAccessCb(CallbackData, prefix="req_acc"):
    chat_id: int

# Решение владельца по запросу доступа.
class ApproveAccessCb(CallbackData, prefix="app_acc"):
    user_id: int
    chat_id: int
    approve: bool  # True — одобрить, False — отклонить


def get_dashboard_kb(servers: list[dict], chat_id: int) -> InlineKeyboardMarkup:
    """Кнопки дашборда: по одной строке на сервер (старт/стоп).

    Принимает те же словари, что и format_dashboard_text — для каждого
    сервера {server_id, display_name, is_online}. Для оффлайн-серверов
    добавляется кнопка подтверждения запуска (если владелец разрешил).
    Внизу — «Обновить» и «Запросить доступ» (для участников без прав).
    """
    buttons: list[list[InlineKeyboardButton]] = []
    for s in servers:
        server_id = int(s.get("id") or 0)
        if not server_id:
            continue
        name = str(s.get("display_name") or f"ID {server_id}")
        is_online = bool(s.get("is_online"))
        row: list[InlineKeyboardButton] = [
            InlineKeyboardButton(
                text=f"🖥 {name}",
                callback_data=ServerCb(server_id=server_id, action="status").pack(),
            )
        ]
        if is_online:
            row.append(
                InlineKeyboardButton(
                    text="⏹ Стоп",
                    callback_data=ServerCb(server_id=server_id, action="stop").pack(),
                )
            )
        else:
            row.append(
                InlineKeyboardButton(
                    text="▶️ Старт",
                    callback_data=ServerCb(server_id=server_id, action="start").pack(),
                )
            )
            row.append(
                InlineKeyboardButton(
                    text="✅ Подтвердить",
                    callback_data=ServerCb(server_id=server_id, action="confirm").pack(),
                )
            )
        buttons.append(row)
    buttons.append(
        [
            InlineKeyboardButton(text="🔄 Обновить", callback_data="refresh_dashboard"),
            InlineKeyboardButton(
                text="🙋 Запросить доступ",
                callback_data=ReqAccessCb(chat_id=chat_id).pack(),
            ),
        ]
    )
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_request_access_kb(chat_id: int) -> InlineKeyboardMarkup:
    """Кнопка «Запросить доступ» для неавторизованных участников чата."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="🙋 Запросить доступ",
                    callback_data=ReqAccessCb(chat_id=chat_id).pack(),
                )
            ]
        ]
    )


def get_approve_access_kb(user_id: int, chat_id: int) -> InlineKeyboardMarkup:
    """Кнопки одобрения/отказа запроса доступа (видны владельцу чата)."""
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(
                    text="✅ Одобрить",
                    callback_data=ApproveAccessCb(user_id=user_id, chat_id=chat_id, approve=True).pack(),
                ),
                InlineKeyboardButton(
                    text="❌ Отклонить",
                    callback_data=ApproveAccessCb(user_id=user_id, chat_id=chat_id, approve=False).pack(),
                ),
            ]
        ]
    )
