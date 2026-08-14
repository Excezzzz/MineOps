"""Онбординг владельца: кука Aternos -> выбор серверов -> аккаунт (multi-tenant).

Поток (в ЛС с ботом):
  1. Пользователь отправляет куку ATERNOS_SESSION — сообщение с кукой
     МГНОВЕННО удаляется (кука не задерживается в переписке);
  2. бот проверяет куку через python-aternos (login_with_session) и показывает
     список серверов аккаунта с чекбоксами (не более max_servers);
  3. после «Готово» создаётся владелец + выбранные серверы (кука шифруется
     Fernet и сохраняется только в БД).

FSM-состояния: waiting_session -> selecting_servers.
"""

from __future__ import annotations

import asyncio
import logging

from aiogram import Router
from aiogram.exceptions import TelegramBadRequest
from aiogram.fsm.context import FSMContext
from aiogram.fsm.state import State, StatesGroup
from aiogram.types import CallbackQuery, Message

import database
from keyboards.onboarding_kb import OnboardingCb, get_server_picker_kb
from services.aternos_api import AternosError, AternosManager
from services.crypto import encrypt_session

logger = logging.getLogger(__name__)

router = Router(name="onboarding")

DEFAULT_MAX_SERVERS = 2  # совпадает с дефолтом owners.max_servers в БД


async def _safe_edit_text(message, text: str, reply_markup=None) -> None:
    """edit_text, который не падает на «message is not modified»."""
    try:
        await message.edit_text(text, reply_markup=reply_markup)
    except TelegramBadRequest:
        pass


class OnboardingState(StatesGroup):
    waiting_session = State()
    selecting_servers = State()


async def start_onboarding(message: Message, state: FSMContext) -> None:
    """Начинает онбординг для нового пользователя (вызывается из /start)."""
    user = message.from_user
    if user is None:
        return
    await state.clear()
    await state.set_state(OnboardingState.waiting_session)
    await state.update_data(username=user.username or "", full_name=user.full_name or "")
    await message.answer(
        "👋 <b>Привет! Я MineOps — бот для управления серверами Aternos.</b>\n\n"
        "Отправьте куку <code>ATERNOS_SESSION</code> — я проверю её, и вы сможете "
        "управлять своими серверами прямо в Telegram.\n\n"
        "💡 <b>Как получить куку:</b> зайдите на aternos.org → откройте консоль "
        "браузера (F12) → вкладка Network → обновите страницу → найдите запрос "
        "на ваш аккаунт → закладка Cookies → скопируйте значение "
        "<code>ATERNOS_SESSION</code>.\n\n"
        "<i>🔒 Кука шифруется (Fernet) и хранится только в вашем профиле бота. "
        "Сообщение с кукой сразу удаляется.</i>"
    )


@router.message(OnboardingState.waiting_session)
async def on_session_cookie(message: Message, state: FSMContext) -> None:
    """Принимает куку, проверяет её и показывает выбор серверов."""
    cookie = (message.text or "").strip()
    user = message.from_user
    if not cookie or user is None:
        return  # не текст — игнорируем

    # Мгновенно удаляем сообщение с кукой.
    try:
        await message.delete()
    except Exception:
        pass

    status_msg = await message.answer("⏳ Проверяю сессию Aternos и ищу серверы...")
    manager = AternosManager(user.id)
    try:
        servers = await asyncio.wait_for(manager.probe_session(cookie), timeout=60)
    except asyncio.TimeoutError:
        await _safe_edit_text(status_msg, "⏱ Таймаут соединения с Aternos. Попробуйте ещё раз.")
        return
    except AternosError as exc:
        await _safe_edit_text(status_msg, f"❌ {exc}")
        return
    except Exception:
        logger.exception("onboarding: сбой проверки куки у %s", user.id)
        await _safe_edit_text(status_msg, "❌ Непредвиденная ошибка. Попробуйте ещё раз.")
        return

    if not servers:
        await _safe_edit_text(
            status_msg,
            "На аккаунте Aternos не найдено серверов. Проверьте аккаунт и попробуйте снова.",
        )
        return

    await state.set_state(OnboardingState.selecting_servers)
    await state.update_data(cookie=cookie, servers=servers, selected=set())
    await _safe_edit_text(
        status_msg,
        "✅ Сессия действительна! Выберите серверы для управления "
        f"(максимум {DEFAULT_MAX_SERVERS}):",
        get_server_picker_kb(servers, set(), DEFAULT_MAX_SERVERS),
    )


@router.callback_query(OnboardingState.selecting_servers, OnboardingCb.filter())
async def on_pick_server(
    callback_query: CallbackQuery,
    callback_data: OnboardingCb,
    state: FSMContext,
) -> None:
    """Чекбоксы выбора серверов и кнопка «Готово»."""
    await callback_query.answer()
    data = await state.get_data()
    servers: list = data.get("servers", [])
    selected: set = set(data.get("selected", []))
    max_servers: int = DEFAULT_MAX_SERVERS

    if callback_data.action == "toggle":
        sid = callback_data.aternos_id
        if sid in selected:
            selected.discard(sid)
        elif len(selected) < max_servers:
            selected.add(sid)
        else:
            await callback_query.answer(f"Можно выбрать не более {max_servers} серверов.")
            return
        await state.update_data(selected=selected)
        await _safe_edit_text(
            callback_query.message,
            "✅ Сессия действительна! Выберите серверы:",
            get_server_picker_kb(servers, selected, max_servers),
        )
        await callback_query.answer()
        return

    if callback_data.action == "done":
        if not selected:
            await callback_query.answer("Выберите хотя бы один сервер.")
            return
        user = callback_query.from_user
        if user is None:
            return
        data = await state.get_data()
        cookie = data.get("cookie", "")
        if not cookie:
            await callback_query.answer("Сессия устарела — начните заново /start.")
            return

        await _safe_edit_text(callback_query.message, "💾 Сохраняю аккаунт...")
        try:
            await database.create_owner(
                user.id,
                username=user.username or "",
                full_name=user.full_name or "",
                aternos_session=encrypt_session(cookie),
                max_servers=max_servers,
            )
        except Exception as exc:
            logger.exception("onboarding: создание владельца не удалось: %s", exc)
            await callback_query.answer("❌ Не удалось создать аккаунт. Попробуйте /start заново.")
            return

        added_names: list[str] = []
        for s in servers:
            if s["aternos_id"] in selected:
                server_id = await database.add_server(
                    user.id, s["aternos_id"], s["server_ip"], s["display_name"]
                )
                if server_id is not None:
                    added_names.append(s["display_name"])
        await database.log_action(user.id, "onboarding", "серверы: " + ", ".join(added_names))

        await state.clear()
        await _safe_edit_text(
            callback_query.message,
            "🎉 <b>Готово!</b> Ваш аккаунт создан.\n\n"
            "1️⃣ Добавьте бота в свою группу и выдайте ему права администратора;\n"
            "2️⃣ В группе напишите <b>/link</b> и выберите серверы;\n"
            "3️⃣ Дашборд со статусами закрепится в чате автоматически.\n\n"
            f"Подключено серверов: <b>{', '.join(added_names) or 'нет'}</b>\n\n"
            "Команды: /panel — панель владельца, /set_session — обновить куку."
        )
