"""Клавиатуры онбординга: выбор серверов владельца из аккаунта Aternos."""

from __future__ import annotations

from aiogram.filters.callback_data import CallbackData
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

# Онбординг: выбор сервера (aternos_id) для добавления в аккаунт владельца.
class OnboardingCb(CallbackData, prefix="onb"):
    action: str          # "toggle" | "done"
    aternos_id: str = ""  # id сервера при toggle


def get_server_picker_kb(
    servers: list[dict], selected: set[str], max_servers: int
) -> InlineKeyboardMarkup:
    """Список серверов с чекбоксами (галочка = выбран, максимум max_servers).

    servers — [{aternos_id, display_name}]; selected — уже выбранные id.
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
                    callback_data=OnboardingCb(action="toggle", aternos_id=sid).pack(),
                )
            ]
        )
    buttons.append(
        [
            InlineKeyboardButton(
                text=f"Готово ({len(selected)}/{max_servers})",
                callback_data=OnboardingCb(action="done").pack(),
            )
        ]
    )
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_done_kb() -> InlineKeyboardMarkup:
    """Заглушка: финальный экран онбординга (на будущее — доп. действия)."""
    return InlineKeyboardMarkup(inline_keyboard=[])
