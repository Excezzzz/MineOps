"""Наблюдатель очереди запуска Aternos (auto_confirm).

После server.start(), если сервер попал в очередь, фоновая задача каждые
15 секунд опрашивает статус через python-aternos (asyncio.to_thread) и, когда
сервер доходит до состояния «ожидает подтверждения», автоматически вызывает
server.confirm(). По ходу обновляет дашборды с позицией в очереди.

Таймаут — 15 минут; при неудаче подтверждения владелец получает уведомление.
"""

from __future__ import annotations

import asyncio
import logging
import time

from aiogram import Bot

import database
from services.aternos_api import AternosError, AternosManager
from services.dashboard import clear_queue_position, set_queue_position, update_dashboards

logger = logging.getLogger(__name__)

POLL_INTERVAL = 15      # сек
WATCH_TIMEOUT = 15 * 60  # 15 минут
MAX_CONSECUTIVE_FAILURES = 5  # подряд неудачных опросов — прекращаем наблюдение

# Активные наблюдатели: server_id -> корутина (предотвращает дубли).
_active_watchers: set[int] = set()


def is_watching(server_id: int) -> bool:
    """Идёт ли уже наблюдение за сервером (защита от повторного запуска)."""
    return server_id in _active_watchers


def start_queue_watcher(bot: Bot, owner_id: int, server_id: int) -> None:
    """Запускает фонового наблюдателя очереди (если ещё не запущен)."""
    if server_id in _active_watchers:
        return
    _active_watchers.add(server_id)
    asyncio.create_task(_watch_loop(bot, owner_id, server_id))


async def _watch_loop(bot: Bot, owner_id: int, server_id: int) -> None:
    """Основной цикл наблюдения; гарантирует очистку при любом исходе."""
    try:
        await _watch(bot, owner_id, server_id)
    except Exception:
        logger.exception("queue watcher для сервера %s упал", server_id)
    finally:
        _active_watchers.discard(server_id)


async def _watch(bot: Bot, owner_id: int, server_id: int) -> None:
    manager = AternosManager(owner_id)
    deadline = time.monotonic() + WATCH_TIMEOUT
    failures = 0
    last_dashboard_update = 0.0

    while time.monotonic() < deadline:
        try:
            status = await manager.get_queue_status(server_id)
            failures = 0
        except AternosError as exc:
            logger.warning("сервер %s: опрос очереди не удался: %s", server_id, exc)
            failures += 1
            if failures >= MAX_CONSECUTIVE_FAILURES:
                await _notify_owner(bot, owner_id, server_id, f"опрос очереди не удался: {exc}")
                break
            await asyncio.sleep(POLL_INTERVAL)
            continue

        raw_queue = status.get("queue")
        server_status = status.get("status")
        server_lang = str(status.get("lang") or "")
        logger.info(
            "server status: status=%r lang=%r queue raw=%r type=%s",
            server_status, server_lang, raw_queue, type(raw_queue).__name__,
        )
        queue_value = _parse_queue(raw_queue)

        # Aternos просит подтверждение запуска — ТОЛЬКО при явном сигнале.
        # Live-проверка (2026-08-13): lang='waiting'/'preparing' — обычная
        # очередь, сервер стартует сам; confirm там отвечает HTTP 400.
        if "confirm" in server_lang.lower():
            try:
                await manager.confirm_server(server_id)
                logger.info("сервер %s: запуск подтверждён (lang=%r)", server_id, server_lang)
                set_queue_position(server_id, 0)
                await _refresh_dashboard(bot, server_id)
            except AternosError as exc:
                logger.warning(
                    "сервер %s: confirm не удался (lang=%r): %s — ждём следующей итерации",
                    server_id, server_lang, exc,
                )
            await asyncio.sleep(POLL_INTERVAL)
            continue

        if server_lang in ("waiting", "preparing"):
            if queue_value is not None and queue_value > 0:
                set_queue_position(server_id, queue_value)
                logger.info("сервер %s: очередь идёт, позиция %s", server_id, queue_value)
            now = time.monotonic()
            if now - last_dashboard_update >= 30:
                await _refresh_dashboard(bot, server_id)
                last_dashboard_update = now
            await asyncio.sleep(POLL_INTERVAL)
            continue

        if server_lang in ("loading", "starting"):
            logger.info("сервер %s: грузится (lang=%r), ждём запуска", server_id, server_lang)
            await asyncio.sleep(POLL_INTERVAL)
            continue

        if server_lang in ("on", "online"):
            logger.info("сервер %s: сервер онлайн, watcher завершён", server_id)
            port = status.get("port")
            if port:
                await database.set_server_port(server_id, int(port))
                logger.info("сервер %s: сохранён mc-порт %s", server_id, port)
            clear_queue_position(server_id)
            await _refresh_dashboard(bot, server_id)
            break

        # Любое другое состояние — логируем и ждём следующую итерацию.
        logger.info("сервер %s: состояние lang=%r, ждём следующей итерации", server_id, server_lang)
        await asyncio.sleep(POLL_INTERVAL)
    else:
        clear_queue_position(server_id)
        await _notify_owner(bot, owner_id, server_id, "таймаут 15 минут: очередь не была пройдена")
        await _refresh_dashboard(bot, server_id)


def _parse_queue(raw_queue) -> int | None:
    """Позиция в очереди из lastStatus['queue'] (python-aternos 3.0.6).

    Значение может быть: int (в т.ч. -1 — ждёт подтверждения), числовая
    строка или dict вида {"count": N, "position": M}. Возвращает int либо
    None, когда очередь не определена (None / 0 / пусто / dict без чисел).
    """
    if raw_queue is None or raw_queue == "":
        return None
    if isinstance(raw_queue, bool):
        return None
    if isinstance(raw_queue, dict):
        for key in ("position", "count"):
            value = raw_queue.get(key)
            if isinstance(value, (int, float)) and not isinstance(value, bool):
                return int(value)
        logger.info("queue dict без числовой позиции: %r", raw_queue)
        return None
    try:
        return int(raw_queue)
    except (TypeError, ValueError):
        logger.warning(
            "неожиданное значение queue: %r (%s)", raw_queue, type(raw_queue).__name__
        )
        return None


async def _refresh_dashboard(bot: Bot, server_id: int) -> None:
    """Обновляет дашборды во всех чатах, где привязан сервер."""
    chats = await database.get_chats_for_server(server_id)
    if chats:
        await update_dashboards(bot)


async def _notify_owner(bot: Bot, owner_id: int, server_id: int, reason: str) -> None:
    """Уведомляет владельца о проблеме с подтверждением запуска."""
    server = await database.get_server(server_id)
    name = server["display_name"] if server else f"ID {server_id}"
    try:
        await bot.send_message(
            owner_id,
            f"⚠️ <b>Автоподтверждение запуска не сработало:</b>\n"
            f"🖥 {name}\n{reason}\n"
            "Проверьте Aternos вручную.",
        )
    except Exception:
        logger.warning("не удалось уведомить владельца %s о сервере %s", owner_id, server_id)
    await database.log_action(owner_id, "auto_confirm_failed", f"{name}: {reason}", server_id=server_id)
