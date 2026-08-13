"""Групповой роутер (multi-tenant): дашборд, кнопки серверов, /link, доступ.

- /link — владелец привязывает чат и ВСЕ свои серверы (дашборд закрепится);
- /unlink — владелец отвязывает чат (дашборд удаляется);
- /status — ручное обновление дашборда (владелец и участники с доступом);
- кнопки дашборда: старт/стоп/подтверждение очереди (ServerCb);
- запрос доступа участника уходит ВЛАДЕЛЬЦУ ЧАТА (ReqAccessCb/ApproveAccessCb);
- /help — динамическая справка по роли.

Управление серверами доступно владельцу чата и участникам с has_access
(кроме lockdown — тогда только владелец). Ошибки Aternos показываются
алертом, бот продолжает работать.
"""

from __future__ import annotations

import asyncio
import logging

from aiogram import F, Router
from aiogram.exceptions import TelegramBadRequest
from aiogram.filters import Command
from aiogram.types import CallbackQuery, Message

import database
from filters.access import HasAccessFilter, IsOwnerFilter
from keyboards.group_kb import (
    ApproveAccessCb,
    ReqAccessCb,
    ServerCb,
    get_approve_access_kb,
    get_dashboard_kb,
)
from services import dashboard, mcsrvstat
from services.aternos_api import AternosError, AternosManager
from services.queue_watcher import start_queue_watcher

logger = logging.getLogger(__name__)

router = Router(name="group")


async def _safe_edit_text(message, text: str, reply_markup=None) -> None:
    """edit_text, который не падает на «message is not modified»."""
    try:
        await message.edit_text(text, reply_markup=reply_markup)
    except TelegramBadRequest:
        pass

ACCESS_REQUEST_COOLDOWN = 300  # сек: не чаще одного запроса доступа в 5 минут
_last_access_request: dict[int, float] = {}


async def _toast(message: Message, text: str, seconds: int = 3) -> None:
    """Короткое всплывающее сообщение, которое само удаляется."""
    try:
        msg = await message.answer(text)
        await asyncio.sleep(seconds)
        await msg.delete()
    except Exception:
        pass


async def _require_chat_permission(callback_query: CallbackQuery) -> tuple | None:
    """Проверяет права на управление сервером в чате кнопки.

    Возвращает (owner, can_manage) или None, если нужно просто ответить.
    """
    user = callback_query.from_user
    if user is None:
        await callback_query.answer()
        return None
    chat_id = int(callback_query.message.chat.id)
    owner = await database.get_chat_owner(chat_id)
    if owner is None:
        await callback_query.answer("Чат не привязан к владельцу.")
        return None
    if owner["user_id"] == user.id:
        return owner, True
    if owner["lockdown_mode"]:
        await callback_query.answer("🔒 Локдаун активен: управление только у владельца.")
        return None
    if not await database.user_can_manage(chat_id, user.id):
        await callback_query.answer("У вас нет доступа. Нажмите «🙋 Запросить доступ».")
        return None
    return owner, True


# ---------------------------------------------------------------------- #
# /link, /unlink, /status, /help
# ---------------------------------------------------------------------- #
@router.message(F.chat.type.in_({"group", "supergroup"}), Command("link"))
async def link_command(message: Message) -> None:
    """Привязка чата: регистрирует чат, привязывает ВСЕ серверы владельца
    и сразу создаёт и закрепляет дашборд (без пикера серверов)."""
    user = message.from_user
    if user is None:
        return
    if not await database.is_owner(user.id):
        await message.answer("❌ Ты не владелец. Пройди онбординг в ЛС с ботом (/start).")
        return

    chat_id = int(message.chat.id)
    chat = await database.get_chat(chat_id)
    if chat and chat["owner_id"] != user.id:
        await message.answer("❌ Этот чат уже привязан к другому владельцу.")
        return

    await database.add_chat(chat_id, user.id, message.chat.title or "")

    servers = await database.get_servers_by_owner(user.id)
    if not servers:
        await message.answer("❌ У тебя нет серверов, добавь через ЛС с ботом (/start).")
        return

    for server in servers:
        await database.link_server_to_chat(chat_id, server["id"])
    await database.log_action(user.id, "chat_link", f"chat {chat_id}", chat_id=chat_id)
    logger.info(
        "чат %s: владелец %s привязал серверы %s",
        chat_id, user.id, [s["id"] for s in servers],
    )

    # Сразу создаём дашборд: статусы -> текст -> отправка -> закреп -> сохранение.
    bound = await database.get_chat_servers(chat_id)
    statuses = await mcsrvstat.get_servers_status(bound)
    merged = [{**s, **statuses.get(s["server_ip"], {})} for s in bound]
    dashboard_text = dashboard.format_dashboard_text(merged)
    kb = get_dashboard_kb(merged, chat_id)
    msg = await message.answer(dashboard_text, reply_markup=kb)
    try:
        await message.bot.pin_chat_message(
            chat_id=chat_id, message_id=msg.message_id, disable_notification=True
        )
    except TelegramBadRequest as exc:
        logger.warning("link: закрепить дашборд не удалось: %s", exc)
    await database.set_chat_pinned_msg(chat_id, msg.message_id)
    logger.info("чат %s: дашборд создан и закреплён (msg %s)", chat_id, msg.message_id)

    await message.answer("✅ Чат привязан, серверы подключены, дашборд закреплён")


@router.message(F.chat.type.in_({"group", "supergroup"}), Command("unlink"), IsOwnerFilter())
async def unlink_command(message: Message) -> None:
    """Владелец отвязывает чат: дашборд останавливается и открепляется."""
    chat_id = int(message.chat.id)
    owner = await database.get_chat_owner(chat_id)
    if owner is None:
        await message.answer("Чат не привязан.")
        return
    pinned = await database.get_chat_pinned_msg(chat_id)
    if pinned:
        try:
            await message.bot.unpin_chat_message(chat_id=chat_id, message_id=pinned)
        except Exception as exc:
            logger.warning("unlink: снять закреп не удалось: %s", exc)
    await database.remove_chat(chat_id)
    await database.log_action(owner["user_id"], "chat_unlink", f"chat {chat_id}", chat_id=chat_id)
    await message.answer("✅ Чат отвязан. Дашборд остановлен.")


@router.message(
    F.chat.type.in_({"group", "supergroup"}), Command("status"), HasAccessFilter()
)
async def status_command(message: Message) -> None:
    """Ручное обновление дашборда (владелец и участники с доступом)."""
    await dashboard._update_chat_dashboard(message.bot, int(message.chat.id))
    await _toast(message, "🔄 Дашборд обновлён.")


@router.message(F.chat.type.in_({"group", "supergroup"}), Command("help"))
async def help_command(message: Message) -> None:
    """Динамическая справка: для владельца — полная, для остальных — базовая."""
    user = message.from_user
    if user is None:
        return
    owner = await database.get_chat_owner(int(message.chat.id))
    if owner and owner["user_id"] == user.id:
        await message.answer(
            "<b>Команды владельца чата:</b>\n"
            "/link — привязать чат и выбрать серверы\n"
            "/unlink — отвязать чат\n"
            "/status — обновить дашборд вручную\n"
            "/emergency — локдаун (в ЛС с ботом)\n\n"
            "<b>Кнопки дашборда:</b>\n"
            "▶️ Старт · ⏹ Стоп · ✅ Подтвердить очередь"
        )
    else:
        await message.answer(
            "<b>Доступные действия:</b>\n"
            "/status — обновить дашборд\n"
            "🙋 Запросить доступ — кнопка под дашбордом\n\n"
            "Управление серверами доступно владельцу и одобренным участникам."
        )


# ---------------------------------------------------------------------- #
# Кнопки дашборда: старт/стоп/подтверждение
# ---------------------------------------------------------------------- #
@router.callback_query(ServerCb.filter())
async def on_server_action(callback_query: CallbackQuery, callback_data: ServerCb) -> None:
    """Обрабатывает кнопки управления сервером на дашборде."""
    if not callback_query.message.chat or callback_query.message.chat.type == "private":
        await callback_query.answer("Кнопки дашборда работают в групповом чате.")
        return
    perm = await _require_chat_permission(callback_query)
    if perm is None:
        return
    owner, _ = perm
    chat_id = int(callback_query.message.chat.id)
    server_id = int(callback_data.server_id)
    action = callback_data.action

    manager = AternosManager(owner["user_id"])
    try:
        if action == "start":
            text = await manager.start_server(server_id)
            dashboard.mark_as_starting(300)
            await callback_query.answer(text)
            start_queue_watcher(callback_query.bot, owner["user_id"], server_id)
            await dashboard.update_chats_dashboards(callback_query.bot, [chat_id])
            other_chats = [
                c["chat_id"]
                for c in await database.get_chats_by_owner(owner["user_id"])
                if int(c["chat_id"]) != chat_id
            ]
            await dashboard.broadcast_message(
                callback_query.bot, other_chats, f"🚀 <b>{text}</b>"
            )
        elif action == "stop":
            text = await manager.stop_server(server_id)
            dashboard.clear_queue_position(server_id)
            await callback_query.answer(text)
            await dashboard.update_chats_dashboards(callback_query.bot, [chat_id])
        elif action == "confirm":
            await manager.confirm_server(server_id)
            dashboard.clear_queue_position(server_id)
            await callback_query.answer("✅ Запуск подтверждён.")
            await dashboard.update_chats_dashboards(callback_query.bot, [chat_id])
    except AternosError as exc:
        await callback_query.answer(str(exc), show_alert=True)
    except Exception as exc:
        logger.exception("server action %s failed: %s", action, exc)
        await callback_query.answer(f"⚠️ Ошибка: {exc}", show_alert=True)


@router.callback_query(F.data == "refresh_dashboard")
async def on_refresh_dashboard(callback_query: CallbackQuery) -> None:
    """Кнопка «Обновить» на дашборде — принудительный апдейт."""
    if callback_query.message.chat and callback_query.message.chat.type != "private":
        await dashboard._update_chat_dashboard(callback_query.bot, int(callback_query.message.chat.id))
        await callback_query.answer("🔄 Дашборд обновлён.")
    else:
        await callback_query.answer()


# ---------------------------------------------------------------------- #
# Запрос и одобрение доступа
# ---------------------------------------------------------------------- #
@router.callback_query(ReqAccessCb.filter())
async def on_request_access(callback_query: CallbackQuery, callback_data: ReqAccessCb) -> None:
    """Участник без доступа запрашивает права у владельца чата."""
    user = callback_query.from_user
    if user is None:
        return
    chat_id = int(callback_data.chat_id)

    owner = await database.get_chat_owner(chat_id)
    if owner is None:
        await callback_query.answer("Чат не привязан к владельцу.")
        return
    if owner["user_id"] == user.id:
        await callback_query.answer("Вы — владелец этого чата 😉")
        return
    if await database.user_can_manage(chat_id, user.id):
        await callback_query.answer("У вас уже есть доступ.")
        return

    now = asyncio.get_event_loop().time()
    if now - _last_access_request.get(user.id, 0) < ACCESS_REQUEST_COOLDOWN:
        await callback_query.answer("Запрос уже отправлен — подождите 5 минут.")
        return
    _last_access_request[user.id] = now

    await database.log_action(
        owner["user_id"],
        "access_request",
        f"user {user.id} (@{user.username or 'нет'})",
        user_id=user.id,
        chat_id=chat_id,
    )
    try:
        await callback_query.bot.send_message(
            owner["user_id"],
            f"🙋 <b>Запрос доступа</b>\n"
            f"Чат: {callback_query.message.chat.title or chat_id}\n"
            f"Пользователь: {user.full_name or '?'} (@{user.username or 'нет'})",
            reply_markup=get_approve_access_kb(user.id, chat_id),
        )
        await callback_query.answer("Запрос отправлен владельцу.")
    except Exception as exc:
        logger.warning("не удалось доставить запрос владельцу %s: %s", owner["user_id"], exc)
        await callback_query.answer("Не удалось отправить запрос владельцу.")


@router.callback_query(ApproveAccessCb.filter())
async def on_approve_access(callback_query: CallbackQuery, callback_data: ApproveAccessCb) -> None:
    """Владелец чата одобряет/отклоняет запрос доступа."""
    user = callback_query.from_user
    if user is None:
        return
    owner = await database.get_chat_owner(int(callback_data.chat_id))
    if owner is None or owner["user_id"] != user.id:
        await callback_query.answer("Только владелец чата может решать этот запрос.")
        return

    if callback_data.approve:
        await database.set_user_access(
            int(callback_data.chat_id), int(callback_data.user_id), True
        )
        await database.log_action(
            owner["user_id"],
            "access_grant",
            f"user {callback_data.user_id}",
            user_id=int(callback_data.user_id),
            chat_id=int(callback_data.chat_id),
        )
        await _safe_edit_text(
            callback_query.message,
            callback_query.message.text + "\n\n✅ <b>Доступ одобрен.</b>",
        )
        try:
            await callback_query.bot.send_message(
                int(callback_data.user_id),
                f"✅ Вам выдан доступ к серверам чата «{callback_query.message.chat.title or callback_data.chat_id}».",
            )
        except Exception:
            pass
    else:
        await database.log_action(
            owner["user_id"],
            "access_deny",
            f"user {callback_data.user_id}",
            user_id=int(callback_data.user_id),
            chat_id=int(callback_data.chat_id),
        )
        await _safe_edit_text(
            callback_query.message,
            callback_query.message.text + "\n\n❌ <b>Отклонено.</b>",
        )
    await callback_query.answer()
