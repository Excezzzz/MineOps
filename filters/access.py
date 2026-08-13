"""Фильтры доступа: владелец чата/аккаунта и авторизованные участники.

Multi-tenant: `IsOwnerFilter` проверяет владельца КОНКРЕТНОГО чата (в ЛС —
владельца бота/аккаунта), `HasAccessFilter` — наличие прав у участника
в конкретном чате (users.has_access) с учётом локдауна владельца.
"""

from __future__ import annotations

from aiogram.filters import BaseFilter
from aiogram.types import CallbackQuery, Message

import database
from config import config


def _chat_id_of(event: Message | CallbackQuery) -> int | None:
    """Извлекает id чата из Message или CallbackQuery (без атрибутов-магии)."""
    if isinstance(event, Message):
        return int(event.chat.id) if event.chat else None
    message = event.message
    if message is None or message.chat is None:
        return None
    return int(message.chat.id)


def _user_id_of(event: Message | CallbackQuery) -> int | None:
    user = event.from_user if hasattr(event, "from_user") else None
    return user.id if user else None


class IsOwnerFilter(BaseFilter):
    """Пропускает только владельца чата (или владельца аккаунта в ЛС)."""

    async def __call__(self, event: Message | CallbackQuery) -> bool:
        user_id = _user_id_of(event)
        if user_id is None:
            return False
        # В ЛС с ботом — владелец аккаунта (глобально).
        if isinstance(event, Message) and event.chat and event.chat.type == "private":
            return await database.is_owner(user_id)
        # В группах — владелец чата (кто связал этот чат).
        chat_id = _chat_id_of(event)
        if chat_id is None:
            return False
        return await database.is_chat_owner(chat_id, user_id)


class HasAccessFilter(BaseFilter):
    """Пропускает авторизованных участников чата (плюс владелец)."""

    async def __call__(self, event: Message | CallbackQuery) -> bool:
        user_id = _user_id_of(event)
        chat_id = _chat_id_of(event)
        if user_id is None or chat_id is None:
            return False

        if await database.is_chat_owner(chat_id, user_id):
            return True

        owner = await database.get_chat_owner(chat_id)
        if owner is None:
            return False
        if owner["lockdown_mode"]:
            return False  # локдаун: управление только у владельца

        return await database.user_can_manage(chat_id, user_id)


class IsSuperAdminFilter(BaseFilter):
    """Пропускает только суперадмина (SUPER_ADMIN_ID)."""

    async def __call__(self, event: Message | CallbackQuery) -> bool:
        user_id = _user_id_of(event)
        return user_id is not None and user_id == config.SUPER_ADMIN_ID
