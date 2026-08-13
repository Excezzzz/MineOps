"""Мидлварь-фаервол (multi-tenant).

Правила:
  * ЛС с ботом — пропускаются все (онбординг и панель владельца);
  * группы/супергруппы — обрабатываются если чат привязан к владельцу
    (есть запись в chats) ЛИБО отправитель — зарегистрированный владелец
    (он может вызвать /link в своём, ещё не привязанном чате);
  * прочие типы (канал и т.п.) — игнорируются.
"""

from __future__ import annotations

import logging
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware
from aiogram.types import Message

import database

logger = logging.getLogger(__name__)


class FirewallMiddleware(BaseMiddleware):
    """Пропускает сообщения из зарегистрированных чатов, ЛС и от владельцев."""

    async def __call__(
        self,
        handler: Callable[[Message, dict[str, Any]], Awaitable[Any]],
        event: Message,
        data: dict[str, Any],
    ) -> Any:
        if event.chat:
            if event.chat.type == "private":
                return await handler(event, data)
            if event.chat.type in ("group", "supergroup"):
                try:
                    if await database.get_chat_owner(int(event.chat.id)):
                        return await handler(event, data)
                    if event.from_user and await database.is_owner(event.from_user.id):
                        return await handler(event, data)
                except Exception as exc:
                    logger.warning("firewall: %s", exc)
                return None  # чат не привязан, отправитель не владелец — игнорируем
        return None
