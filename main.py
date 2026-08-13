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


async def update_dashboards_job(bot: Bot) -> None:
    """Обновляет дашборды всех владельцев (вызывается шедулером)."""
    from services.dashboard import update_dashboards

    await update_dashboards(bot)


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
        scheduler.start()
        logger.info("scheduler: дашборды каждые %s c", UPDATE_INTERVAL)

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
