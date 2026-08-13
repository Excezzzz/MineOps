"""Живой дашборд статусов (multi-tenant, принцип единого закрепа).

Каждые 30 секунд (шедулер в main.py) для каждого владельца опрашиваются
статусы его серверов через mcsrvstat.us, и в каждом привязанном чате
обновляется ЕДИНЫЙ закреплённый дашборд (edit_message_text). Если сообщение
удалено или закрепа ещё нет — дашборд отправляется и закрепляется заново.

Также здесь живёт рассылка broadcast_message по списку чатов (например,
уведомление о запуске сервера) и временные позиции очереди запуска.
"""

from __future__ import annotations

import logging
import time
from html import escape as quote_html

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError

import database
from keyboards.group_kb import get_dashboard_kb
from services import mcsrvstat

logger = logging.getLogger(__name__)

# Момент (unix-time), до которого серверы считаются «запускающимися».
_starting_until = 0

# Позиции в очереди Aternos по server_id (заполняет queue_watcher).
_queue_positions: dict[int, int] = {}


def mark_as_starting(seconds: int = 300) -> None:
    """Помечает серверы как запускающиеся на ближайшие `seconds` секунд."""
    global _starting_until
    _starting_until = time.time() + seconds


def clear_starting() -> None:
    """Сбрасывает пометку «запускается»."""
    global _starting_until
    _starting_until = 0


def is_starting() -> bool:
    """True, если серверы ещё в состоянии «запускается»."""
    return time.time() < _starting_until


def set_queue_position(server_id: int, position: int) -> None:
    """Запоминает позицию сервера в очереди запуска (для дашборда)."""
    _queue_positions[server_id] = position


def clear_queue_position(server_id: int) -> None:
    """Убирает позицию очереди (сервер стартовал или сдался)."""
    _queue_positions.pop(server_id, None)


def format_dashboard_text(servers: list[dict]) -> str:
    """Формирует ЕДИНЫЙ дашборд: подзаголовок и сводка для каждого сервера.

    servers — список словарей: {server_id, display_name, server_ip,
    is_online, players_online, players_max, player_names, version}.
    """
    if not servers:
        return "🔴 <b>В чате нет подключённых серверов.</b>\nВладелец: /link"

    blocks: list[str] = []
    for s in servers:
        ip = str(s.get("server_ip") or "Не указан")
        name = str(s.get("display_name") or ip)
        server_id = int(s.get("id") or 0)
        is_online = bool(s.get("is_online"))

        if is_online:
            clear_starting()
            clear_queue_position(server_id)
            status_line = f"🟢 <b>{quote_html(name)} — ОНЛАЙН</b>"
        else:
            position = _queue_positions.get(server_id, 0)
            if position > 0:
                status_line = (
                    f"🟡 <b>{quote_html(name)} — В ОЧЕРЕДИ (позиция {position})</b> ⏳\n"
                    "<i>(Ожидание запуска на Aternos...)</i>"
                )
            elif is_starting():
                status_line = (
                    f"🟡 <b>{quote_html(name)} — ЗАПУСКАЕТСЯ / В ОЧЕРЕДИ...</b> ⏳\n"
                    "<i>(Открытие портов Aternos занимает ~2-5 мин)</i>"
                )
            else:
                status_line = f"🔴 <b>{quote_html(name)} — ОФФЛАЙН</b>"

        players_online = s.get("players_online", 0)
        players_max = s.get("players_max", 0)
        player_names = s.get("player_names") or []
        who_plays = ", ".join(quote_html(str(n)) for n in player_names[:10])
        if not who_plays:
            who_plays = "Нет игроков"

        blocks.append(
            "\n".join(
                [
                    status_line,
                    f"🌐 IP: {quote_html(ip)}",
                    f"👥 Игроки: {players_online}/{players_max}",
                    f"📜 Кто играет: {who_plays}",
                    f"📦 Версия: {quote_html(str(s.get('version', 'Неизвестно')) or 'Неизвестно')}",
                ]
            )
        )
    return "\n\n".join(blocks)


async def _render_dashboard(bot: Bot, chat_id: int, text: str, kb) -> None:
    """Обновляет или (пере)создаёт закреплённый дашборд в одном чате.

    Любые Telegram-ошибки (сообщение удалено, нет прав на закрепление)
    перехватываются, чтобы сбой в одном чате не ронял остальные.
    """
    pinned_msg_id = await database.get_chat_pinned_msg(chat_id)

    if pinned_msg_id is not None:
        try:
            await bot.edit_message_text(
                text=text, chat_id=chat_id, message_id=pinned_msg_id, reply_markup=kb
            )
            return
        except TelegramAPIError as exc:
            if "message is not modified" in str(exc):
                return  # контент не изменился — дашборд актуален
            logger.warning("chat %s: edit dashboard failed (%s), will re-send", chat_id, exc)
        # Падаем вниз и пересоздаём сообщение.

    try:
        msg = await bot.send_message(chat_id=chat_id, text=text, reply_markup=kb)
        await bot.pin_chat_message(
            chat_id=chat_id, message_id=msg.message_id, disable_notification=True
        )
        await database.set_chat_pinned_msg(chat_id, msg.message_id)
        logger.info("чат %s: дашборд создан и закреплён (msg %s)", chat_id, msg.message_id)
    except (TelegramAPIError, AttributeError) as exc:
        logger.warning("chat %s: cannot create dashboard (%s)", chat_id, exc)


async def _update_chat_dashboard(bot: Bot, chat_id: int) -> None:
    """Обновляет дашборд одного чата (по привязанным серверам)."""
    servers = await database.get_chat_servers(chat_id)
    if not servers:
        return  # владелец ещё не привязал серверы — дашборд не нужен

    servers = [{**s, "port": await database.get_server_port(s["id"])} for s in servers]
    statuses = await mcsrvstat.get_servers_status(servers)
    for s in servers:
        status = statuses.get(s["server_ip"], {})
        known_port = await database.get_server_port(s["id"])
        status_port = status.get("port")
        if status_port and status_port != known_port:
            await database.set_server_port(s["id"], status_port)
            logger.info(
                "сервер %s (id=%s): порт обновлён на %s", s["server_ip"], s["id"], status_port
            )
    merged = [{**s, **statuses.get(s["server_ip"], {})} for s in servers]
    await _render_dashboard(bot, chat_id, format_dashboard_text(merged), get_dashboard_kb(merged, chat_id))


async def update_dashboards(bot: Bot) -> None:
    """Обновляет дашборды всех владельцев в их привязанных чатах.

    У каждого владельца независимый опрос его серверов (per-owner polling).
    Статус опрашивается для каждого сервера владельца даже без привязанных
    чатов — диагностические логи показывают реальную картину.
    """
    logger.info("=== DASHBOARD TICK ===")
    owners = await database.get_all_owners()
    logger.info("owners: %d", len(owners))
    for owner in owners:
        servers = await database.get_servers_by_owner(owner["user_id"])
        logger.info("owner %s: %d серверов", owner["user_id"], len(servers))
        for server in servers:
            chats = await database.get_chats_for_server(server["id"])
            logger.info(
                "server %s (id=%s): %d чатов",
                server["server_ip"], server["id"], len(chats),
            )
            port = await database.get_server_port(server["id"])
            status = await mcsrvstat.get_server_status(server["server_ip"], port=port)
            logger.info(
                "server %s: online=%s (players %s/%s, port=%s)",
                server["server_ip"],
                status.get("is_online"),
                status.get("players_online"),
                status.get("players_max"),
                port,
            )
            if status.get("is_online"):
                clear_starting()
                clear_queue_position(server["id"])
            status_port = status.get("port")
            if status_port and status_port != port:
                await database.set_server_port(server["id"], status_port)
                logger.info(
                    "server %s (id=%s): порт обновлён на %s",
                    server["server_ip"], server["id"], status_port,
                )
        for chat in await database.get_chats_by_owner(owner["user_id"]):
            await _update_chat_dashboard(bot, int(chat["chat_id"]))


async def update_chats_dashboards(bot: Bot, chat_ids: list[int]) -> None:
    """Обновляет дашборды в конкретных чатах (после запуска/остановки)."""
    for chat_id in chat_ids:
        await _update_chat_dashboard(bot, chat_id)


async def broadcast_message(bot: Bot, chat_ids: list[int], text: str) -> None:
    """Рассылает `text` в указанные чаты (ошибки отдельных чатов не фатальны)."""
    for chat_id in chat_ids:
        try:
            await bot.send_message(chat_id=chat_id, text=text)
        except Exception as exc:
            logger.warning("broadcast: не удалось отправить в chat %s: %s", chat_id, exc)
