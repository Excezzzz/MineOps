"""MineOps — Telegram-бот для управления Minecraft-серверами на Aternos
(multi-tenant: каждый владелец управляет своими серверами).

Точка входа: БД (с миграциями) -> бот/диспетчер -> мидлвари -> шедулер ->
поллинг. Фон:
  * live-дашборды: обновление каждые UPDATE_INTERVAL секунд (mcsrvstat.us);
  * глобальный перехватчик ошибок шлёт алерт суперадмину.

Порядок роутеров важен: onboarding -> admin -> group (групповые callback-и
не пересекаются с панелью, но приоритет у личного меню).
"""

from __future__ import annotations

import asyncio
import logging
import os
from html import escape as quote_html

from aiogram import Bot, Dispatcher
from aiogram.client.default import DefaultBotProperties
from aiogram.types import BotCommand, ErrorEvent
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.interval import IntervalTrigger

import database
from config import config
from handlers import admin, group, onboarding
from middlewares.firewall import FirewallMiddleware
from middlewares.register import RegisterMiddleware

os.makedirs("data", exist_ok=True)
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)-8s %(name)s | %(message)s",
    handlers=[
        logging.StreamHandler(),
        logging.FileHandler("data/mineops.log", encoding="utf-8"),
    ],
)
logger = logging.getLogger(__name__)

UPDATE_INTERVAL = 30  # сек: период обновления дашбордов
SESSION_CHECK_INTERVAL = 5 * 60  # сек: период проверки кук владельцев

# Диагностика утечек памяти: TRACE_MEM=1 включает tracemalloc и каждые
# MEM_TRACE_EVERY_TICKS тиков печатает топ аллокаций в лог.
if os.getenv("TRACE_MEM") == "1":
    import tracemalloc

    tracemalloc.start(25)
    logger.info("tracemalloc включён (диагностика памяти)")
MEM_TRACE_EVERY_TICKS = 10


def _log_mem_top() -> None:
    """Логирует топ аллокаций tracemalloc (только при TRACE_MEM=1)."""
    if not (os.getenv("TRACE_MEM") == "1" and tracemalloc.is_tracing()):
        return
    try:
        snapshot = tracemalloc.take_snapshot()
        for stat in snapshot.statistics("lineno")[:8]:
            frame = stat.traceback.format()[0]
            logger.info(
                "MEMTOP %.1f MB / %d allocs <- %s",
                stat.size / 1e6, stat.count, frame,
            )
    except Exception as exc:
        logger.warning("MEMTOP: снимок не удался: %s", exc)


async def update_dashboards_job(bot: Bot) -> None:
    """Обновляет дашборды всех владельцев (вызывается шедулером)."""
    from services.dashboard import update_dashboards

    _mem_tick_count = getattr(update_dashboards_job, "_mem_ticks", 0) + 1
    setattr(update_dashboards_job, "_mem_ticks", _mem_tick_count)
    if _mem_tick_count % MEM_TRACE_EVERY_TICKS == 0:
        _log_mem_top()
    await update_dashboards(bot)


async def check_sessions_job(bot: Bot) -> None:
    """Проверяет куки владельцев; уведомляет в ЛС о просроченных."""
    from services.dashboard import check_all_sessions

    await check_all_sessions(bot)


async def main() -> None:
    """Главная корутина: БД -> бот -> мидлвари -> роутеры -> шедулер -> поллинг."""
    bot = Bot(config.BOT_TOKEN, default=DefaultBotProperties(parse_mode="HTML"))
    dp = Dispatcher()

    dp.message.middleware(FirewallMiddleware())
    dp.message.middleware(RegisterMiddleware())
    dp.include_routers(onboarding.router, admin.router, group.router)

    scheduler = AsyncIOScheduler()

    @dp.error()
    async def global_error_handler(event: ErrorEvent) -> None:
        """Логирует ошибки и шлёт алерт суперадмину (не роняет бота)."""
        exc = event.exception
        logger.exception("глобальная ошибка: %s", exc)
        try:
            await bot.send_message(
                config.SUPER_ADMIN_ID,
                f"🚨 <b>Глобальная ошибка бота:</b>\n"
                f"<pre>{quote_html(str(exc)[:1500])}</pre>",
            )
        except Exception:
            pass

    @dp.startup()
    async def on_startup(bot: Bot, **kwargs) -> None:
        """Инициализация при старте: БД, команды, шедулер (aiogram 3.30 API)."""
        logger.info("инициализация БД...")
        await database.init_db()
        logger.info("БД готова (схема v%s)", database.SCHEMA_VERSION)

        await bot.set_my_commands(
            [
                BotCommand(command="start", description="Панель / Онбординг"),
                BotCommand(command="panel", description="Панель владельца"),
                BotCommand(command="help", description="Помощь"),
                BotCommand(command="status", description="Обновить дашборд (в чате)"),
                BotCommand(command="link", description="Привязать чат и серверы (владелец)"),
                BotCommand(command="unlink", description="Отвязать чат (владелец)"),
                BotCommand(command="emergency", description="Локдаун (владелец)"),
                BotCommand(command="set_session", description="Обновить куку (владелец)"),
            ]
        )

        scheduler.add_job(
            update_dashboards_job,
            IntervalTrigger(seconds=UPDATE_INTERVAL),
            args=[bot],
            id="dashboards",
            replace_existing=True,
            max_instances=1,
        )
        scheduler.add_job(
            check_sessions_job,
            IntervalTrigger(seconds=SESSION_CHECK_INTERVAL),
            args=[bot],
            id="sessions",
            replace_existing=True,
            max_instances=1,
        )
        scheduler.start()
        logger.info("scheduler: дашборды каждые %s c, проверка кук каждые %s c",
                    UPDATE_INTERVAL, SESSION_CHECK_INTERVAL)

    @dp.shutdown()
    async def on_shutdown(bot: Bot, **kwargs) -> None:
        scheduler.shutdown(wait=False)
        await database.close_db()
        logger.info("бот остановлен")

    try:
        await dp.start_polling(bot)
    finally:
        await bot.session.close()


if __name__ == "__main__":
    asyncio.run(main())
