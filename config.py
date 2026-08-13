"""Конфигурация MineOps: загрузка настроек из .env (python-dotenv).

Multi-tenant версия: жёсткие привязки к конкретному серверу Aternos
(SERVER_IP / ATERNOS_SESSION / ATERNOS_SERVER) убраны — каждый владелец
приносит свою куку через онбординг. Остаются только базовые настройки
бота, суперадмина и шифрования.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from dotenv import load_dotenv

# Каталог проекта — рядом с ним лежит .env
BASE_DIR = Path(__file__).resolve().parent
load_dotenv(BASE_DIR / ".env")


@dataclass(frozen=True)
class Config:
    """Типизированные настройки бота и пути к локальным файлам."""

    BOT_TOKEN: str          # токен Telegram-бота (от @BotFather)
    SUPER_ADMIN_ID: int     # ID суперадмина (владельца бота, для служебных функций)
    ENCRYPTION_KEY: str = os.getenv("ENCRYPTION_KEY", "")  # Fernet-ключ (опционально)
    DB_PATH: str = "data/mineops.db"  # путь к SQLite-файлу

    @classmethod
    def load(cls) -> "Config":
        """Читает переменные окружения и валидирует обязательные значения."""
        token = os.getenv("BOT_TOKEN", "").strip()
        if not token:
            raise ValueError("BOT_TOKEN is missing in .env")
        super_admin_raw = os.getenv("SUPER_ADMIN_ID", "").strip()
        try:
            super_admin_id = int(super_admin_raw)
        except ValueError:
            super_admin_id = 0
        if super_admin_id <= 0:
            raise ValueError("SUPER_ADMIN_ID is missing or invalid in .env")
        return cls(
            BOT_TOKEN=token,
            SUPER_ADMIN_ID=super_admin_id,
            ENCRYPTION_KEY=os.getenv("ENCRYPTION_KEY", "").strip(),
        )


# Глобальный объект конфигурации: `from config import config`
config = Config.load()
