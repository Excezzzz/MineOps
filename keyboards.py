"""MineOps - inline keyboards with paginated user lists.

Callback data is colon-separated for parsing in main.py:
    chats:p:{page}                    chat selection page
    users:{chat_id}:p:{page}          user list page (5 per page)
    user:{chat_id}:{user_id}          user management card
    grant|revoke:{chat_id}:{user_id}  grant/revoke access
    start|stop|backup:owner           owner control actions
    lockdown / lockdown:confirm       emergency lockdown (two-step)
    dash:start                        start from dashboard (access-checked)
"""

from __future__ import annotations

from math import ceil
from typing import List

from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

PAGE_SIZE = 5

ACTION_CHATS = "chats"
ACTION_USER_LIST = "users"
ACTION_USER_CARD = "user"
ACTION_GRANT = "grant"
ACTION_REVOKE = "revoke"
ACTION_START = "start"
ACTION_STOP = "stop"
ACTION_BACKUP = "backup"
ACTION_LOCKDOWN = "lockdown"
ACTION_LOCKDOWN_CONFIRM = "lockdown:confirm"
ACTION_CLOSE = "close"
ACTION_DASH_START = "dash:start"
ACTION_NOOP = "noop:0"


def _clamp_page(page: int, total: int) -> int:
    return min(max(page, 0), max(1, ceil(total / PAGE_SIZE)) - 1)


def _nav_row(prefix: str, arg: str, page: int, total_pages: int) -> List[InlineKeyboardButton]:
    return [
        InlineKeyboardButton(
            text="◀️ Prev",
            callback_data=f"{prefix}:{arg}:p:{page - 1}" if page > 0 else ACTION_NOOP,
        ),
        InlineKeyboardButton(text=f"📄 {page + 1}/{total_pages}", callback_data=ACTION_NOOP),
        InlineKeyboardButton(
            text="Next ▶️",
            callback_data=f"{prefix}:{arg}:p:{page + 1}" if page < total_pages - 1 else ACTION_NOOP,
        ),
    ]


def chats_menu_kb(chats: List[dict], page: int = 0) -> InlineKeyboardMarkup:
    page = _clamp_page(page, len(chats))
    start, end = page * PAGE_SIZE, min((page + 1) * PAGE_SIZE, len(chats))

    rows: List[List[InlineKeyboardButton]] = [
        [InlineKeyboardButton(
            text=f"💬 {chat['title'] or chat['chat_id']}",
            callback_data=f"{ACTION_USER_LIST}:{chat['chat_id']}:p:0",
        )]
        for chat in chats[start:end]
    ]
    if len(chats) > PAGE_SIZE:
        rows.append(_nav_row(ACTION_CHATS, "", page, ceil(len(chats) / PAGE_SIZE)))
    if not chats:
        rows.append([InlineKeyboardButton(text="no chats yet", callback_data=ACTION_NOOP)])
    rows.append([
        InlineKeyboardButton(text="⚡️ Start Server", callback_data=f"{ACTION_START}:owner"),
        InlineKeyboardButton(text="🛑 Stop Server", callback_data=f"{ACTION_STOP}:owner"),
    ])
    rows.append([
        InlineKeyboardButton(text="💾 Backup Now", callback_data=f"{ACTION_BACKUP}:owner"),
        InlineKeyboardButton(text="🚨 Emergency Lockdown", callback_data=ACTION_LOCKDOWN),
    ])
    rows.append([InlineKeyboardButton(text="❌ Close", callback_data=ACTION_CLOSE)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def users_kb(users: List[dict], chat_id: int, page: int = 0, owner_id: int = 0) -> InlineKeyboardMarkup:
    page = _clamp_page(page, len(users))
    start, end = page * PAGE_SIZE, min((page + 1) * PAGE_SIZE, len(users))

    rows: List[List[InlineKeyboardButton]] = [
        [InlineKeyboardButton(
            text=(
                f"[{'🟢' if u['has_access'] else '🔴'}] "
                f"{'👑 ' if u['user_id'] == owner_id else ''}"
                f"{u['username'] or u['first_name'] or u['user_id']}"
            ),
            callback_data=f"{ACTION_USER_CARD}:{chat_id}:{u['user_id']}",
        )]
        for u in users[start:end]
    ]
    if len(users) > PAGE_SIZE:
        rows.append(_nav_row(ACTION_USER_LIST, str(chat_id), page, ceil(len(users) / PAGE_SIZE)))
    rows.append([
        InlineKeyboardButton(text="⬅️ Back to chats", callback_data=f"{ACTION_CHATS}:p:0"),
        InlineKeyboardButton(text="❌ Close", callback_data=ACTION_CLOSE),
    ])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def user_card_kb(chat_id: int, user_id: int, has_access: bool, owner_id: int = 0) -> InlineKeyboardMarkup:
    if user_id == owner_id:
        rows: List[List[InlineKeyboardButton]] = [
            [InlineKeyboardButton(text="👑 Owner - permanent access", callback_data=ACTION_NOOP)],
        ]
    else:
        action, label = (ACTION_REVOKE, "❌ Revoke Access") if has_access else (ACTION_GRANT, "✅ Grant Access")
        rows = [[InlineKeyboardButton(text=label, callback_data=f"{action}:{chat_id}:{user_id}")]]
    rows.append([
        InlineKeyboardButton(text="⬅️ Back", callback_data=f"{ACTION_USER_LIST}:{chat_id}:p:0"),
        InlineKeyboardButton(text="❌ Close", callback_data=ACTION_CLOSE),
    ])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def dashboard_kb() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="⚡️ Start Server", callback_data=ACTION_DASH_START)],
    ])


def lockdown_kb() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="⚠️ CONFIRM LOCKDOWN", callback_data=ACTION_LOCKDOWN_CONFIRM)],
        [InlineKeyboardButton(text="Cancel", callback_data=ACTION_CLOSE)],
    ])


def close_kb() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="❌ Close", callback_data=ACTION_CLOSE)],
    ])