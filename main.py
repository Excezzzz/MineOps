"""MineOps - Telegram bot for Aternos Minecraft server management."""

from __future__ import annotations

import asyncio
import logging
import os
from datetime import datetime, timezone
from typing import List, Optional

from aiogram import Bot, Dispatcher, F, Router
from aiogram.client.default import DefaultBotProperties
from aiogram.enums import ParseMode
from aiogram.exceptions import TelegramBadRequest, TelegramNotFound
from aiogram.filters import Command, CommandObject, CommandStart
from aiogram.types import CallbackQuery, Message
from aiogram.utils.html import quote_html
from dotenv import load_dotenv

from aternos_service import AternosError, AternosService, ServerInfo
from database import Database
from keyboards import (
    ACTION_BACKUP,
    ACTION_CHATS,
    ACTION_CLOSE,
    ACTION_DASH_START,
    ACTION_GRANT,
    ACTION_LOCKDOWN,
    ACTION_LOCKDOWN_CONFIRM,
    ACTION_NOOP,
    ACTION_REVOKE,
    ACTION_START,
    ACTION_STOP,
    ACTION_USER_CARD,
    ACTION_USER_LIST,
    chats_menu_kb,
    close_kb,
    dashboard_kb,
    lockdown_kb,
    user_card_kb,
    users_kb,
)

load_dotenv()

logging.basicConfig(
    level=os.getenv("LOG_LEVEL", "INFO").upper(),
    format="%(asctime)s %(levelname)-8s %(name)s %(message)s",
)
logger = logging.getLogger("mineops")

BOT_TOKEN = os.getenv("BOT_TOKEN", "")
OWNER_ID = int(os.getenv("OWNER_ID", "0") or 0)
UPDATE_SECONDS = max(5.0, float(os.getenv("DASHBOARD_UPDATE_SECONDS", "15")))

db = Database()
bot: Optional[Bot] = None
aternos: Optional[AternosService] = None
_tasks: List[asyncio.Task] = []
router = Router()


def _bot() -> Bot:
    assert bot is not None
    return bot


def _aternos() -> AternosService:
    assert aternos is not None
    return aternos


def _utc_time() -> str:
    return datetime.now(timezone.utc).strftime("%H:%M:%S")


def _parse_ints(data: str, n: int) -> List[int]:
    values = [int(p) for p in data.split(":") if p.isdigit() or (p.startswith("-") and p[1:].isdigit())]
    if len(values) != n:
        raise ValueError(f"bad callback payload: {data!r}")
    return values


def render_server_status(info: ServerInfo) -> str:
    addr = f"<code>{quote_html(info.address)}:{info.port}</code>"
    if info.online:
        return (
            "🟢 <b>Server Status:</b> Online\n"
            f"🌐 <b>IP:</b> {addr}\n"
            f"👥 <b>Online Players:</b> <b>{info.players}</b>/{info.max_players}\n"
            f"🧱 <b>Version:</b> {quote_html(info.version) or 'unknown'}\n"
            f"⚡ <b>Latency:</b> {info.latency_ms:.0f} ms\n"
        )
    return (
        "🔴 <b>Server Status:</b> Offline\n"
        f"🌐 <b>IP:</b> {addr}\n"
        "👥 <b>Online Players:</b> 0\n"
        f"ℹ️ <b>Info:</b> {quote_html(info.error) or 'not reachable'}\n"
    )


def render_user_card(user: dict) -> str:
    name = quote_html(user["username"] or user["first_name"] or str(user["user_id"]))
    state = "🟢 Has access" if user["has_access"] else "🔴 No access"
    handle = f"@<b>{quote_html(user['username'])}</b>\n" if user["username"] else ""
    return f"👤 <b>{name}</b>\n🆔 <code>{user['user_id']}</code>\n{handle}🔑 {state}"


async def _notify_user_privately(user_id: int, text: str) -> None:
    if await db.get_user(user_id, user_id) is None:
        return
    try:
        await _bot().send_message(user_id, text)
    except (TelegramBadRequest, TelegramNotFound):
        pass


async def _register_sender(message: Message) -> None:
    user = message.from_user
    if user is None:
        return
    await db.upsert_chat(message.chat.id, message.chat.title or "", message.chat.type)
    await db.upsert_user(message.chat.id, user.id, user.username or "", user.first_name or "")
    if user.id == OWNER_ID:
        await db.set_access(message.chat.id, user.id, True)


# ---------------------------------------------------------------------- #
# commands
# ---------------------------------------------------------------------- #
@router.message(CommandStart())
async def cmd_start(message: Message) -> None:
    await _register_sender(message)
    user_id = message.from_user.id if message.from_user else 0

    if user_id == OWNER_ID:
        chats = await db.list_chats()
        await message.answer(
            "🎛 <b>Owner menu</b>\nSelect a chat to manage its users.",
            reply_markup=chats_menu_kb(chats, page=0),
        )
        return

    if await db.has_access(message.chat.id, user_id):
        await message.answer(
            render_server_status(await _aternos().get_status()),
            reply_markup=dashboard_kb(),
        )
    else:
        await message.answer(
            "⛔ <b>Access denied.</b> You cannot control this server. "
            "Ask the owner to grant you access."
        )


@router.message(Command("emergency"))
async def cmd_emergency(message: Message) -> None:
    if message.from_user is None or message.from_user.id != OWNER_ID:
        return
    await message.answer(
        "🚨 <b>Emergency Lockdown</b>\n\n"
        "Revokes <b>access for ALL users</b> except you. "
        "Re-grant access later from the owner menu.",
        reply_markup=lockdown_kb(),
    )


@router.message(Command("setbackup"))
async def cmd_setbackup(message: Message, command: CommandObject) -> None:
    if message.from_user is None or message.from_user.id != OWNER_ID:
        return
    try:
        hours = float((command.args or "").strip())
    except ValueError:
        await message.answer("Usage: <code>/setbackup 6</code>  (hours, 0.5 - 720)")
        return
    if not 0.5 <= hours <= 720:
        await message.answer("⛔ Hours must be between <b>0.5</b> and <b>720</b>.")
        return
    await db.set_backup_hours(hours)
    await message.answer(f"💾 Auto-backups set to every <b>{hours:g}</b> hour(s). "
                         "Applies from the next backup cycle.")


# ---------------------------------------------------------------------- #
# owner admin callbacks
# ---------------------------------------------------------------------- #
@router.callback_query(F.data.startswith(f"{ACTION_CHATS}:p:"))
async def cb_chats_page(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    chats = await db.list_chats()
    await cb.message.edit_text(
        "🎛 <b>Owner menu</b>\nSelect a chat to manage its users.",
        reply_markup=chats_menu_kb(chats, page=_parse_ints(cb.data, 1)[0]),
    )
    await cb.answer()


@router.callback_query(F.data.startswith(f"{ACTION_USER_LIST}:") and F.data.contains(":p:"))
async def cb_users_page(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    chat_id, page = _parse_ints(cb.data, 2)
    chat = await db.get_chat(chat_id)
    title = quote_html(chat["title"]) if chat and chat["title"] else f"chat {chat_id}"
    users = await db.list_users(chat_id)
    await cb.message.edit_text(
        f"💬 <b>{title}</b> - {len(users)} user(s)",
        reply_markup=users_kb(users, chat_id, page=page),
    )
    await cb.answer()


@router.callback_query(F.data.startswith(f"{ACTION_USER_CARD}:"))
async def cb_user_card(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    chat_id, user_id = _parse_ints(cb.data, 2)
    user = await db.get_user(chat_id, user_id)
    if user is None:
        await cb.answer("User not found.", show_alert=True)
        return
    await cb.message.edit_text(
        render_user_card(user),
        reply_markup=user_card_kb(chat_id, user_id, bool(user["has_access"])),
    )
    await cb.answer()


@router.callback_query(F.data.startswith(f"{ACTION_GRANT}:") | F.data.startswith(f"{ACTION_REVOKE}:"))
async def cb_set_access(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    grant = cb.data.startswith(f"{ACTION_GRANT}:")
    chat_id, user_id = _parse_ints(cb.data, 2)
    await db.set_access(chat_id, user_id, grant)
    user = await db.get_user(chat_id, user_id)
    await cb.message.edit_text(
        render_user_card(user) if user else "User row missing.",
        reply_markup=user_card_kb(chat_id, user_id, grant),
    )
    action = "granted" if grant else "revoked"
    await _notify_user_privately(user_id, f"🔐 MineOps: access {action} in chat <code>{chat_id}</code>.")
    await cb.answer(f"✅ Access {action}")


@router.callback_query(F.data == ACTION_LOCKDOWN)
async def cb_lockdown(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    await cb.message.edit_text(
        "🚨 <b>Emergency Lockdown</b>\n\n"
        "This instantly revokes <b>access for ALL users</b> except you.",
        reply_markup=lockdown_kb(),
    )
    await cb.answer()


@router.callback_query(F.data == ACTION_LOCKDOWN_CONFIRM)
async def cb_lockdown_confirm(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    revoked = await db.revoke_all_except_owner(OWNER_ID)
    await cb.message.edit_text(
        f"🚨 <b>LOCKDOWN ACTIVE</b>\n\nAccess revoked for <b>{revoked}</b> user(s). "
        "Only the owner can control the server now. Re-grant access from the owner menu.",
        reply_markup=close_kb(),
    )
    await cb.answer("🔒 Lockdown engaged")


@router.callback_query(F.data == ACTION_NOOP)
async def cb_noop(cb: CallbackQuery) -> None:
    await cb.answer()


@router.callback_query(F.data == ACTION_CLOSE)
async def cb_close(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer()
        return
    try:
        await cb.message.delete()
    except (TelegramBadRequest, TelegramNotFound):
        try:
            await cb.message.edit_text("❌ Closed.", reply_markup=None)
        except (TelegramBadRequest, TelegramNotFound):
            pass
    await cb.answer()


# ---------------------------------------------------------------------- #
# server control callbacks
# ---------------------------------------------------------------------- #
async def _run_server_action(cb: CallbackQuery, verb: str) -> None:
    await cb.answer(f"⏳ {verb}…")
    try:
        if verb == "Stop":
            result = await _aternos().stop_server()
        elif verb == "Backup":
            result = await _aternos().create_backup()
        else:
            result = await _aternos().start_server()
        await cb.message.answer(result)
    except AternosError as exc:
        await cb.message.answer(f"❌ {quote_html(str(exc))}")


@router.callback_query(F.data == f"{ACTION_START}:owner")
@router.callback_query(F.data == f"{ACTION_STOP}:owner")
async def cb_owner_control(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    await _run_server_action(cb, "Stop" if cb.data == f"{ACTION_STOP}:owner" else "Start")


@router.callback_query(F.data == f"{ACTION_BACKUP}:owner")
async def cb_owner_backup(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID:
        await cb.answer("⛔ Owner only.", show_alert=True)
        return
    await _run_server_action(cb, "Backup")


@router.callback_query(F.data == ACTION_DASH_START)
async def cb_dash_start(cb: CallbackQuery) -> None:
    if cb.from_user.id != OWNER_ID and not await db.has_access(cb.message.chat.id, cb.from_user.id):
        await cb.answer("⛔ Access denied. Only users granted access by the owner can start the server.",
                        show_alert=True)
        return
    await _run_server_action(cb, "Start")


# ---------------------------------------------------------------------- #
# background loops
# ---------------------------------------------------------------------- #
async def _publish_dashboard(text: str) -> None:
    msg = await _bot().send_message(OWNER_ID, text, reply_markup=dashboard_kb())
    try:
        await _bot().pin_chat_message(OWNER_ID, msg.message_id, disable_notification=True)
    except (TelegramBadRequest, TelegramNotFound):
        logger.warning("could not pin dashboard")
    await db.set_pinned_message(msg.message_id)


async def _recreate_dashboard(text: str) -> None:
    # Old pinned message is gone (deleted/expired): clean it up and re-pin.
    pinned_id = await db.get_pinned_message()
    if pinned_id is not None:
        for attempt in (
            lambda: _bot().unpin_chat_message(OWNER_ID, pinned_id),
            lambda: _bot().delete_message(OWNER_ID, pinned_id),
        ):
            try:
                await attempt()
            except (TelegramBadRequest, TelegramNotFound):
                pass
        await db.unset_pinned_message()
    await _publish_dashboard(text)


async def dashboard_loop() -> None:
    await asyncio.sleep(5)
    while True:
        try:
            info = await _aternos().get_status()
            text = render_server_status(info) + f"🕒 <b>Updated:</b> {_utc_time()} UTC"

            pinned_id = await db.get_pinned_message()
            if pinned_id is None:
                await _publish_dashboard(text)
            else:
                try:
                    await _bot().edit_message_text(
                        text, chat_id=OWNER_ID, message_id=pinned_id, reply_markup=dashboard_kb()
                    )
                except TelegramBadRequest as exc:
                    if "message is not modified" not in str(exc):
                        logger.warning("dashboard edit failed (%s); recreating", exc)
                        await _recreate_dashboard(text)
                except TelegramNotFound:
                    await _recreate_dashboard(text)
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception("dashboard loop error")
        await asyncio.sleep(UPDATE_SECONDS)


async def backup_loop() -> None:
    await asyncio.sleep(20)
    while True:
        await asyncio.sleep(max(await db.get_backup_hours(), 0.5) * 3600)
        try:
            result = await _aternos().create_backup()
            await _bot().send_message(OWNER_ID, f"💾 <b>Backup complete</b>\n{quote_html(result)}")
        except AternosError as exc:
            logger.warning("automated backup failed: %s", exc)
            await _bot().send_message(OWNER_ID, f"⚠️ <b>Backup failed</b>\n{quote_html(str(exc))}")
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception("automated backup failed")
            await _bot().send_message(OWNER_ID, "⚠️ <b>Backup failed</b> with an unexpected error.")


# ---------------------------------------------------------------------- #
# lifecycle
# ---------------------------------------------------------------------- #
async def _on_startup() -> None:
    await db.init()
    if await db.get_setting("backup_hours") is None:
        await db.set_backup_hours(float(os.getenv("DEFAULT_BACKUP_HOURS", "24")))
    _tasks.append(asyncio.create_task(dashboard_loop()))
    _tasks.append(asyncio.create_task(backup_loop()))
    await _bot().send_message(OWNER_ID, "🚀 <b>MineOps online.</b>")


async def _on_shutdown() -> None:
    for task in _tasks:
        task.cancel()
    await asyncio.gather(*_tasks, return_exceptions=True)
    if aternos is not None:
        await aternos.shutdown()
    await db.close()


async def main() -> None:
    global bot, aternos

    if not BOT_TOKEN or OWNER_ID <= 0:
        raise SystemExit("BOT_TOKEN and OWNER_ID must be configured in .env")

    bot = Bot(token=BOT_TOKEN, default=DefaultBotProperties(parse_mode=ParseMode.HTML))
    aternos = AternosService(
        username=os.getenv("ATERNOS_USERNAME", ""),
        password=os.getenv("ATERNOS_PASSWORD", ""),
        session=os.getenv("ATERNOS_SESSION") or None,
        server_address=os.getenv("SERVER_ADDRESS", "localhost"),
        server_port=int(os.getenv("SERVER_PORT", "25565")),
    )

    dp = Dispatcher()
    dp.include_router(router)
    dp.startup.register(_on_startup)
    dp.shutdown.register(_on_shutdown)

    await dp.start_polling(bot, allowed_updates=dp.resolve_used_update_types())


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except (KeyboardInterrupt, SystemExit):
        pass