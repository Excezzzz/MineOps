"""Личная панель владельца и суперадмина (DM, multi-tenant).

/start — если пользователь уже владелец: панель; иначе: онбординг.
/panel — панель владельца (серверы, чаты, настройки, аудит).
/set_session — обновление куки Aternos (шифруется, кеш клиента сбрасывается).
/emergency — переключение lockdown владельца (с рассылкой по чатам).
/announce — рассылка суперадмина всем владельцам.

Панель владельца:
  * Серверы — карточки с действиями (старт/стоп/подтверждение/удаление);
  * Чаты — карточки чатов, список участников с пагинацией, отвязка;
  * Настройки — автоподтверждение, lockdown;
  * Аудит — последние действия владельца.
"""

from __future__ import annotations

import asyncio
import logging
from html import escape as quote_html

from aiogram import F, Router
from aiogram.exceptions import TelegramBadRequest
from aiogram.filters import Command, CommandStart
from aiogram.fsm.context import FSMContext
from aiogram.fsm.state import State, StatesGroup
from aiogram.types import CallbackQuery, Message

import database
from filters.access import IsSuperAdminFilter
from keyboards.admin_kb import (
    DeleteAccountCb,
    OwnerSettingsCb,
    PanelActionCb,
    PanelCb,
    PanelChatCb,
    PanelServerCb,
    RefreshServerCb,
    UsersPageCb,
    get_chat_card_kb,
    get_delete_account_kb,
    get_owner_audit_kb,
    get_owner_chats_kb,
    get_owner_panel_kb,
    get_owner_servers_kb,
    get_owner_settings_kb,
    get_refresh_servers_kb,
    get_server_card_kb,
    get_users_page_kb,
)
from services import dashboard, mcsrvstat
from services.aternos_api import AternosError, AternosManager
from services.queue_watcher import start_queue_watcher
from handlers.onboarding import start_onboarding

logger = logging.getLogger(__name__)

router = Router(name="admin")
router.message.filter(F.chat.type == "private")

USERS_PAGE_SIZE = 10


class SessionRefreshState(StatesGroup):
    """FSM: ждём новую куку Aternos (кнопка «Обновить куку» в панели)."""

    waiting_cookie = State()


# Список серверов аккаунта и текущий выбор для «Обновить серверы».
_refresh_servers: dict[int, list[dict]] = {}
_refresh_selection: dict[int, set[str]] = {}


async def _safe_edit_text(message, text: str, reply_markup=None) -> None:
    """edit_text, который не падает на «message is not modified»."""
    try:
        await message.edit_text(text, reply_markup=reply_markup)
    except TelegramBadRequest:
        pass


async def _panel_text(user_id: int) -> str:
    owner = await database.get_owner(user_id)
    server_count = len(await database.get_active_servers_by_owner(user_id))
    chat_count = len(await database.get_chats_by_owner(user_id))
    lockdown = "🔒 вкл" if owner and owner["lockdown_mode"] else "выкл"
    return (
        "🛠 <b>Панель владельца</b>\n\n"
        f"👤 {quote_html(owner['full_name'] or str(user_id))}\n"
        f"🖥 Серверы: {server_count}\n"
        f"💬 Чаты: {chat_count}\n"
        f"🔒 Локдаун: {lockdown}\n\n"
        "Выберите раздел:"
    )


async def _require_owner(callback_query: CallbackQuery, owner_id: int) -> bool:
    if not await database.is_owner(owner_id):
        await callback_query.answer("Нет доступа: вы не владелец.")
        return False
    return True


# ---------------------------------------------------------------------- #
# /start, /panel
# ---------------------------------------------------------------------- #
@router.message(CommandStart())
async def cmd_start(message: Message, state) -> None:
    """Владелец -> панель; новый пользователь -> онбординг."""
    user = message.from_user
    if user is None:
        return
    if await database.is_owner(user.id):
        await database.update_owner_profile(user.id, user.username or "", user.full_name or "")
        await message.answer(await _panel_text(user.id), reply_markup=get_owner_panel_kb())
    else:
        await start_onboarding(message, state)


@router.message(Command("panel"))
async def cmd_panel(message: Message) -> None:
    user = message.from_user
    if user is None:
        return
    if not await database.is_owner(user.id):
        await message.answer("Сначала пройдите онбординг: отправьте куку ATERNOS_SESSION.")
        return
    await message.answer(await _panel_text(user.id), reply_markup=get_owner_panel_kb())


# ---------------------------------------------------------------------- #
# Панель: навигация
# ---------------------------------------------------------------------- #
@router.callback_query(PanelCb.filter())
async def on_panel(
    callback_query: CallbackQuery, callback_data: PanelCb, state: FSMContext
) -> None:
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    action = callback_data.action

    if action == "servers":
        servers = await database.get_active_servers_by_owner(user.id)
        text = "<b>Ваши серверы:</b>\n" if servers else "<b>Ваши серверы:</b>\nНет серверов. Пройдите онбординг заново: /start."
        await _safe_edit_text(callback_query.message, text, get_owner_servers_kb(servers))
    elif action == "chats":
        chats = await database.get_chats_by_owner(user.id)
        text = "<b>Ваши чаты:</b>\n" if chats else "<b>Ваши чаты:</b>\nНет чатов. Добавьте бота в группу и напишите /link."
        await _safe_edit_text(callback_query.message, text, get_owner_chats_kb(chats))
    elif action == "settings":
        owner = await database.get_owner(user.id)
        servers = await database.get_active_servers_by_owner(user.id)
        auto_confirm = bool(servers[0]["auto_confirm"]) if servers else False
        await _safe_edit_text(
            callback_query.message,
            "⚙️ <b>Настройки</b> (применяются ко всем серверам):",
            get_owner_settings_kb(bool(owner["lockdown_mode"]), auto_confirm),
        )
    elif action == "audit":
        logs = await database.get_audit_log(user.id, limit=10)
        if not logs:
            text = "📋 <b>Аудит:</b>\nПока нет записей."
        else:
            lines = [f"📋 <b>Аудит:</b>"]
            for entry in logs:
                lines.append(
                    f"• {quote_html(entry['action'])} — {quote_html(entry['details'] or '-')}"
                )
            text = "\n".join(lines)
        await _safe_edit_text(callback_query.message, text, get_owner_audit_kb())
    elif action == "refresh_servers":
        manager = AternosManager(user.id)
        try:
            aternos_servers = await asyncio.wait_for(
                manager.list_account_servers(), timeout=60
            )
        except asyncio.TimeoutError:
            await _safe_edit_text(
                callback_query.message, "⏱ Таймаут Aternos, попробуйте ещё раз.",
                get_owner_panel_kb(),
            )
            return
        except AternosError as exc:
            await _safe_edit_text(callback_query.message, f"❌ {exc}", get_owner_panel_kb())
            return
        if not aternos_servers:
            await _safe_edit_text(
                callback_query.message, "❌ На аккаунте Aternos нет серверов.",
                get_owner_panel_kb(),
            )
            return
        existing = await database.get_servers_by_owner(user.id)
        existing_ids = {s["aternos_id"] for s in existing}
        fetched_ids = {s["aternos_id"] for s in aternos_servers}
        _refresh_servers[user.id] = aternos_servers
        _refresh_selection[user.id] = existing_ids & fetched_ids
        await _safe_edit_text(
            callback_query.message,
            "🔄 Серверы аккаунта Aternos.\n"
            "☑️ отмечены подключённые. Снимите галочку — сервер отключится, "
            "поставьте — подключится.",
            get_refresh_servers_kb(aternos_servers, _refresh_selection[user.id]),
        )
    elif action == "refresh_session":
        await state.set_state(SessionRefreshState.waiting_cookie)
        await _safe_edit_text(
            callback_query.message,
            "🔑 Отправь новую куку <code>ATERNOS_SESSION</code> — "
            "сообщение будет сразу удалено.",
        )
    elif action == "delete_account":
        await _safe_edit_text(
            callback_query.message,
            "🗑 <b>Удалить аккаунт?</b>\n\n"
            "Будут удалены все серверы и отвязаны все чаты. "
            "Онбординг можно будет пройти заново через /start.",
            get_delete_account_kb(),
        )
    elif action == "back":
        await _safe_edit_text(
            callback_query.message,
            await _panel_text(user.id),
            get_owner_panel_kb(),
        )
    await callback_query.answer()


# ---------------------------------------------------------------------- #
# Панель: серверы
# ---------------------------------------------------------------------- #
@router.callback_query(PanelServerCb.filter())
async def on_panel_server(callback_query: CallbackQuery, callback_data: PanelServerCb) -> None:
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    server = await database.get_server(int(callback_data.server_id))
    if server is None or server["owner_id"] != user.id:
        await callback_query.answer("Сервер не найден.")
        return
    status = await mcsrvstat.get_server_status(server["server_ip"])
    is_online = bool(status["is_online"])
    state_text = "🟢 ОНЛАЙН" if is_online else "🔴 ОФФЛАЙН"
    await _safe_edit_text(
        callback_query.message,
        f"🖥 <b>{quote_html(server['display_name'])}</b>\n"
        f"🌐 IP: {quote_html(server['server_ip'] or '—')}\n"
        f"{state_text} · "
        f"{status['players_online']}/{status['players_max']} игроков\n"
        f"✅ Автоподтверждение: {'вкл' if server['auto_confirm'] else 'выкл'}\n\n"
        f"<i>Настройка автоподтверждения — в разделе «Настройки».</i>",
        get_server_card_kb(server["id"], is_online),
    )
    await callback_query.answer()


@router.callback_query(PanelActionCb.filter())
async def on_panel_action(callback_query: CallbackQuery, callback_data: PanelActionCb) -> None:
    """Действия над сервером из панели владельца."""
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    server = await database.get_server(int(callback_data.server_id))
    if server is None or server["owner_id"] != user.id:
        await callback_query.answer("Сервер не найден.")
        return

    manager = AternosManager(user.id)
    action = callback_data.action
    try:
        if action == "start":
            text = await manager.start_server(server["id"])
            dashboard.mark_as_starting(300)
            start_queue_watcher(callback_query.bot, user.id, server["id"])
            await callback_query.answer(text)
            await dashboard.update_chats_dashboards(
                callback_query.bot,
                [int(c["chat_id"]) for c in await database.get_chats_by_owner(user.id)],
            )
        elif action == "stop":
            text = await manager.stop_server(server["id"])
            dashboard.clear_queue_position(server["id"])
            await callback_query.answer(text)
            await dashboard.update_chats_dashboards(
                callback_query.bot,
                [int(c["chat_id"]) for c in await database.get_chats_by_owner(user.id)],
            )
        elif action == "confirm":
            await manager.confirm_server(server["id"])
            dashboard.clear_queue_position(server["id"])
            await callback_query.answer("✅ Запуск подтверждён.")
            await dashboard.update_chats_dashboards(
                callback_query.bot,
                [int(c["chat_id"]) for c in await database.get_chats_by_owner(user.id)],
            )
        elif action == "delete":
            chats = await database.get_chats_for_server(server["id"])
            chat_ids = [int(c["chat_id"]) for c in chats]
            await database.unbind_server_from_all_chats(server["id"])
            await database.deactivate_server(server["id"])
            await database.log_action(user.id, "server_deactivate", server["display_name"])
            await callback_query.answer("Сервер отключён.")
            servers = await database.get_active_servers_by_owner(user.id)
            await _safe_edit_text(
                callback_query.message,
                "<b>Ваши серверы:</b>",
                get_owner_servers_kb(servers),
            )
            if chat_ids:
                await dashboard.update_chats_dashboards(callback_query.bot, chat_ids)
    except AternosError as exc:
        await callback_query.answer(str(exc), show_alert=True)
    except Exception as exc:
        logger.exception("panel action %s failed: %s", action, exc)
        await callback_query.answer(f"⚠️ Ошибка: {exc}", show_alert=True)


@router.callback_query(RefreshServerCb.filter())
async def on_refresh_server(
    callback_query: CallbackQuery, callback_data: RefreshServerCb
) -> None:
    """Галочки при обновлении серверов; «Готово» применяет изменения в БД."""
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    servers = _refresh_servers.get(user.id)
    if servers is None:
        await callback_query.answer(
            "Сессия обновления устарела — нажмите «🔄 Обновить серверы» заново."
        )
        return
    selected = _refresh_selection.setdefault(user.id, set())

    if callback_data.action == "toggle":
        sid = callback_data.aternos_id
        if sid in selected:
            selected.discard(sid)
        else:
            selected.add(sid)
        await _safe_edit_text(
            callback_query.message,
            "🔄 Серверы аккаунта Aternos.\n"
            "☑️ отмечены подключённые. Снимите галочку — сервер отключится, "
            "поставьте — подключится.",
            get_refresh_servers_kb(servers, selected),
        )
        return

    _refresh_servers.pop(user.id, None)
    _refresh_selection.pop(user.id, None)
    if callback_data.action == "cancel":
        await _safe_edit_text(
            callback_query.message, await _panel_text(user.id), get_owner_panel_kb()
        )
        return

    existing = await database.get_servers_by_owner(user.id)
    by_id = {s["aternos_id"]: s for s in existing}
    added: list[str] = []
    removed: list[str] = []
    touched_chat_ids: set[int] = set()
    for s in servers:
        aid = s["aternos_id"]
        if aid in selected:
            if aid in by_id:
                if not by_id[aid]["is_active"]:
                    await database.set_server_active(by_id[aid]["id"], True)
            else:
                server_id = await database.add_server(
                    user.id, aid, s["server_ip"], s["display_name"]
                )
                if server_id is not None:
                    added.append(s["display_name"])
        elif aid in by_id and by_id[aid]["is_active"]:
            chats = await database.get_chats_for_server(by_id[aid]["id"])
            touched_chat_ids.update(int(c["chat_id"]) for c in chats)
            await database.unbind_server_from_all_chats(by_id[aid]["id"])
            await database.deactivate_server(by_id[aid]["id"])
            removed.append(by_id[aid]["display_name"])
    await database.log_action(
        user.id, "servers_refresh", f"+{', '.join(added)} -{', '.join(removed)}"
    )
    await _safe_edit_text(
        callback_query.message, await _panel_text(user.id), get_owner_panel_kb()
    )
    if touched_chat_ids:
        await dashboard.update_chats_dashboards(
            callback_query.bot, sorted(touched_chat_ids)
        )
    if added or removed:
        await callback_query.answer(
            "\n".join(
                ([f"➕ Добавлены: {', '.join(added)}"] if added else [])
                + ([f"➖ Отключены: {', '.join(removed)}"] if removed else [])
            )
        )


@router.callback_query(DeleteAccountCb.filter())
async def on_delete_account(
    callback_query: CallbackQuery, callback_data: DeleteAccountCb
) -> None:
    """Подтверждение «🗑 Удалить аккаунт»: полное удаление владельца."""
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    if not callback_data.confirm:
        await _safe_edit_text(
            callback_query.message, await _panel_text(user.id), get_owner_panel_kb()
        )
        return
    chats = await database.get_chats_by_owner(user.id)
    pm_pinned = await database.get_owner_pm_pinned(user.id)
    await database.delete_owner(user.id)
    for chat in chats:
        try:
            await callback_query.bot.unpin_chat_message(
                chat_id=int(chat["chat_id"]), message_id=chat["pinned_msg_id"]
            )
        except Exception:
            pass
    if pm_pinned:
        try:
            await callback_query.bot.unpin_chat_message(
                chat_id=user.id, message_id=pm_pinned
            )
        except Exception:
            pass
    await _safe_edit_text(
        callback_query.message,
        "🗑 <b>Аккаунт удалён.</b>\nНапишите /start, чтобы пройти онбординг заново.",
    )


@router.message(SessionRefreshState.waiting_cookie)
async def on_new_cookie_message(message: Message, state: FSMContext) -> None:
    """Принимает новую куку: удаляет сообщение, проверяет и сохраняет."""
    user = message.from_user
    cookie = (message.text or "").strip()
    try:
        await message.delete()
    except Exception:
        pass
    await state.clear()
    if user is None or not cookie:
        return
    manager = AternosManager(user.id)
    try:
        await asyncio.wait_for(manager.probe_session(cookie), timeout=60)
    except asyncio.TimeoutError:
        await message.answer("⏱ Таймаут Aternos. Попробуйте ещё раз.")
        return
    except AternosError as exc:
        await message.answer(f"❌ Кука недействительна: {exc}")
        return
    await manager.update_session(cookie)
    await message.answer("✅ Кука обновлена! Кеш клиента сброшен.")


# ---------------------------------------------------------------------- #
# Панель: чаты и участники
# ---------------------------------------------------------------------- #
@router.callback_query(PanelChatCb.filter())
async def on_panel_chat(callback_query: CallbackQuery, callback_data: PanelChatCb) -> None:
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    chat_id = int(callback_data.chat_id)
    chat = await database.get_chat(chat_id)
    if chat is None or chat["owner_id"] != user.id:
        await callback_query.answer("Чат не найден или не принадлежит вам.")
        return

    if callback_data.action == "select":
        servers = await database.get_chat_servers(chat_id)
        linked = ", ".join(
            s["display_name"] for s in servers
        ) or "нет"
        await _safe_edit_text(
            callback_query.message,
            f"💬 <b>{quote_html(chat['title'] or str(chat_id))}</b>\n"
            f"ID: <code>{chat_id}</code>\n"
            f"Серверы: {quote_html(linked)}\n\n"
            "Управление привязками — командой /link в самом чате.",
            get_chat_card_kb(chat_id),
        )
    elif callback_data.action == "unlink":
        pinned = await database.get_chat_pinned_msg(chat_id)
        if pinned:
            try:
                await callback_query.bot.unpin_chat_message(chat_id=chat_id, message_id=pinned)
            except Exception:
                pass
        await database.remove_chat(chat_id)
        await database.log_action(user.id, "chat_unlink", f"chat {chat_id}", chat_id=chat_id)
        await callback_query.answer("Чат отвязан.")
        chats = await database.get_chats_by_owner(user.id)
        await _safe_edit_text(
            callback_query.message,
            "<b>Ваши чаты:</b>",
            get_owner_chats_kb(chats),
        )
        return
    elif callback_data.action == "users":
        await _show_users_page(callback_query, user.id, chat_id, 0)
        return
    await callback_query.answer()


@router.callback_query(UsersPageCb.filter())
async def on_users_page(callback_query: CallbackQuery, callback_data: UsersPageCb) -> None:
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    chat_id = int(callback_data.chat_id)
    chat = await database.get_chat(chat_id)
    if chat is None or chat["owner_id"] != user.id:
        await callback_query.answer("Чат не найден или не принадлежит вам.")
        return
    await _show_users_page(callback_query, user.id, chat_id, max(int(callback_data.page), 0))


async def _show_users_page(callback_query: CallbackQuery, owner_id: int, chat_id: int, page: int) -> None:
    total = await database.get_chat_users_count(chat_id)
    offset = page * USERS_PAGE_SIZE
    users = await database.get_chat_users_paginated(chat_id, USERS_PAGE_SIZE, offset)
    lines = [f"👥 <b>Участники чата</b> (всего: {total})"]
    for u in users:
        access = "✅" if u["has_access"] else "—"
        name = u["full_name"] or (f"@{u['username']}" if u["username"] else f"id{u['user_id']}")
        lines.append(f"{access} {quote_html(name)}")
    if not users:
        lines.append("Пока никто не зарегистрирован.")
    await _safe_edit_text(
        callback_query.message,
        "\n".join(lines),
        get_users_page_kb(chat_id, page, total, USERS_PAGE_SIZE),
    )


# ---------------------------------------------------------------------- #
# Панель: настройки
# ---------------------------------------------------------------------- #
@router.callback_query(OwnerSettingsCb.filter())
async def on_owner_settings(callback_query: CallbackQuery, callback_data: OwnerSettingsCb) -> None:
    user = callback_query.from_user
    await callback_query.answer()
    if user is None or not await _require_owner(callback_query, user.id):
        return
    if callback_data.action == "lockdown":
        await database.set_owner_lockdown(user.id, bool(callback_data.enabled))
        await database.log_action(
            user.id, "lockdown", f"{'вкл' if callback_data.enabled else 'выкл'}"
        )
        chats = [int(c["chat_id"]) for c in await database.get_chats_by_owner(user.id)]
        state_text = "🔒 Локдаун включён: управление серверами приостановлено." if callback_data.enabled else "✅ Локдаун снят."
        await dashboard.broadcast_message(callback_query.bot, chats, state_text)
        await callback_query.answer(state_text)
    elif callback_data.action == "auto_confirm":
        servers = await database.get_active_servers_by_owner(user.id)
        for server in servers:
            await database.set_server_auto_confirm(server["id"], bool(callback_data.enabled))
        await database.log_action(
            user.id, "auto_confirm", f"все серверы: {'вкл' if callback_data.enabled else 'выкл'}"
        )
        await callback_query.answer(
            f"Автоподтверждение: {'вкл' if callback_data.enabled else 'выкл'} (все серверы)"
        )
    await _refresh_settings(callback_query, user.id)


async def _refresh_settings(callback_query: CallbackQuery, owner_id: int) -> None:
    owner = await database.get_owner(owner_id)
    servers = await database.get_active_servers_by_owner(owner_id)
    auto_confirm = bool(servers[0]["auto_confirm"]) if servers else False
    await _safe_edit_text(
        callback_query.message,
        "⚙️ <b>Настройки</b> (применяются ко всем серверам):",
        get_owner_settings_kb(bool(owner["lockdown_mode"]), auto_confirm),
    )


# ---------------------------------------------------------------------- #
# /set_session, /emergency, /announce
# ---------------------------------------------------------------------- #
@router.message(Command("set_session"))
async def cmd_set_session(message: Message) -> None:
    """Обновление куки Aternos владельца (в ЛС)."""
    user = message.from_user
    if user is None or not await database.is_owner(user.id):
        await message.answer("❌ Сначала пройдите онбординг (/start).")
        return
    parts = message.text.split(maxsplit=1)
    cookie = parts[1].strip() if len(parts) > 1 else ""
    if not cookie:
        await message.answer("Использование: /set_session <кука ATERNOS_SESSION>")
        return
    try:
        await message.delete()
    except Exception:
        pass
    manager = AternosManager(user.id)
    try:
        await manager.update_session(cookie)
        await message.answer("✅ Кука обновлена! Кеш клиента сброшен.")
    except AternosError as exc:
        await message.answer(f"❌ {exc}")


@router.message(Command("emergency"))
async def cmd_emergency(message: Message) -> None:
    """Переключение lockdown: все чаты владельца получают уведомление."""
    user = message.from_user
    if user is None or not await database.is_owner(user.id):
        await message.answer("❌ Только владелец может управлять локдауном.")
        return
    owner = await database.get_owner(user.id)
    new_state = not bool(owner["lockdown_mode"])
    await database.set_owner_lockdown(user.id, new_state)
    await database.log_action(user.id, "lockdown", f"{'вкл' if new_state else 'выкл'}")
    chats = [int(c["chat_id"]) for c in await database.get_chats_by_owner(user.id)]
    if new_state:
        text = "🔒 <b>Локдаун включён:</b> управление серверами приостановлено до команды /emergency."
    else:
        text = "✅ <b>Локдаун снят:</b> управление серверами снова доступно."
    await dashboard.broadcast_message(message.bot, chats, text)
    await message.answer(text)


@router.message(Command("announce"), IsSuperAdminFilter())
async def cmd_announce(message: Message) -> None:
    """Рассылка суперадмина всем владельцам."""
    parts = message.text.split(maxsplit=1)
    text = parts[1].strip() if len(parts) > 1 else ""
    if not text:
        await message.answer("Использование: /announce <текст>")
        return
    owners = await database.get_all_owners()
    sent = 0
    for owner in owners:
        try:
            await message.bot.send_message(owner["user_id"], f"📢 {text}")
            sent += 1
        except Exception as exc:
            logger.warning("announce: владельцу %s не доставлено: %s", owner["user_id"], exc)
    await message.answer(f"Рассылка отправлена {sent}/{len(owners)} владельцам.")


@router.callback_query(F.data == "noop")
async def on_noop(callback_query: CallbackQuery) -> None:
    """Заглушка для неактивных кнопок (пагинация, пустые списки)."""
    await callback_query.answer()
