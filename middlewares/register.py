"""Мидлварь регистрации участников (multi-tenant).

Каждое сообщение из группы, привязанной к владельцу, обновляет/создаёт
запись участника в таблице `users` (для проверки доступа). Владельцы
не трогаются: их роль определяется таблицей `owners`.
"""

from __future__ import annotations

import logging
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware
from aiogram.types import Message

import database

logger = logging.getLogger(__name__)


class RegisterMiddleware(BaseMiddleware):
    """Авторегистрация участников в таблице users для каждого чата."""

    async def __call__(
        self,
        handler: Callable[[Message, dict[str, Any]], Awaitable[Any]],
        event: Message,
        data: dict[str, Any],
    ) -> Any:
        if event.chat and event.chat.type in ("group", "supergroup") and event.from_user:
            try:
                # Регистрируем только если чат привязан к владельцу.
                if await database.get_chat_owner(int(event.chat.id)):
                    await database.upsert_chat_user(
                        chat_id=int(event.chat.id),
                        user_id=event.from_user.id,
                        username=event.from_user.username or "",
                        full_name=event.from_user.full_name or "",
                    )
            except Exception as exc:
                logger.warning("register middleware: %s", exc)
        return await handler(event, data)
