"""MineOps — асинхронное multi-tenant хранилище SQLite (aiosqlite).

Схема БД (версия 2 — multi-tenant):
  db_version   — версия схемы и время её применения (миграции);
  owners       — владельцы: у каждого свой Aternos-аккаунт, зашифрованная
                 кука (Fernet), свои настройки и lockdown;
  servers      — серверы Aternos владельцев (уникальны в паре owner+aternos_id);
  chats        — привязанные к владельцу групповые чаты (один owner на чат);
  chat_servers — связка «чат <-> сервер»: какие серверы показываются в чате;
  users        — участники чатов и их права на управление серверами;
  audit_log    — журнал действий владельцев (для панели и отладки).

Версионирование: migrate_db() приводит файл БД к текущей версии схемы.
При обнаружении старой (личной) схемы версии 1 её таблицы переименовываются
в *_legacy без потери данных, и создаётся новая схема.
"""

from __future__ import annotations

import os
from datetime import datetime, timezone
from typing import List, Optional

import aiosqlite

SCHEMA_VERSION = 4


def _now_iso() -> str:
    """Текущее время в UTC в формате ISO (для created_at/updated_at)."""
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _default_db_path() -> str:
    """Путь к БД: из .env (DB_PATH) или стандартный data/mineops.db."""
    return os.getenv("DB_PATH", "data/mineops.db")


class Database:
    """Обёртка над aiosqlite: инициализация схемы и CRUD для всех таблиц."""

    def __init__(self, db_path: str | None = None) -> None:
        self.db_path = db_path or _default_db_path()
        self._conn: Optional[aiosqlite.Connection] = None

    # ------------------------------------------------------------------ #
    # соединение
    # ------------------------------------------------------------------ #
    @property
    def conn(self) -> aiosqlite.Connection:
        if self._conn is None:
            raise RuntimeError("call init_db() before using Database")
        return self._conn

    async def init_db(self) -> None:
        """Открывает БД и приводит схему к актуальной версии."""
        dirname = os.path.dirname(self.db_path)
        if dirname:
            os.makedirs(dirname, exist_ok=True)
        self._conn = await aiosqlite.connect(self.db_path)
        self._conn.row_factory = aiosqlite.Row
        await self._conn.execute("PRAGMA journal_mode = WAL")
        await self._conn.execute("PRAGMA busy_timeout = 5000")
        await self._conn.execute("PRAGMA foreign_keys = ON")
        await self._migrate()

    async def close(self) -> None:
        """Закрывает соединение (вызывается при остановке бота)."""
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    # ------------------------------------------------------------------ #
    # миграции
    # ------------------------------------------------------------------ #
    async def _table_names(self) -> set[str]:
        cur = await self.conn.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table'"
        )
        return {str(row["name"]) for row in await cur.fetchall()}

    async def _table_columns(self, table: str) -> set[str]:
        cur = await self.conn.execute(f"PRAGMA table_info({table})")
        return {str(row["name"]) for row in await cur.fetchall()}

    async def _current_version(self) -> int:
        tables = await self._table_names()
        if "db_version" not in tables:
            return 0
        cur = await self.conn.execute("SELECT MAX(version) AS v FROM db_version")
        row = await cur.fetchone()
        return int(row["v"]) if row and row["v"] is not None else 0

    async def _set_version(self, version: int) -> None:
        await self.conn.execute(
            "INSERT INTO db_version (version, applied_at) VALUES (?, ?)",
            (version, _now_iso()),
        )
        await self.conn.commit()  # фиксируем немедленно: миграции должны быть устойчивыми

    async def _migrate(self) -> None:
        """Приводит схему к SCHEMA_VERSION с сохранением старых данных."""
        version = await self._current_version()

        # Версия 0 — «сырая» БД: старая личная схема без db_version.
        if version == 0:
            await self._migrate_v0_to_v1()
            version = 1

        if version < 2:
            await self._create_v2_schema()
            await self._set_version(2)
            version = 2

        if version < 3:
            # v3: последний известный Minecraft-порт сервера Aternos.
            # Порт Aternos меняется при каждом запуске и нужен для legacy-ping.
            await self.conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS server_meta (
                    server_id  INTEGER PRIMARY KEY
                               REFERENCES servers(id) ON DELETE CASCADE,
                    mc_port    INTEGER,
                    updated_at TEXT NOT NULL DEFAULT ''
                );
                """
            )
            await self.conn.commit()
            await self._set_version(3)
            version = 3

        if version < 4:
            # v4: закреплённый дашборд владельца в ЛС с ботом
            # (pm_pinned_msg_id — id сообщения-дашборда в личке владельца).
            columns = await self._table_columns("owners")
            if "pm_pinned_msg_id" not in columns:
                await self.conn.execute(
                    "ALTER TABLE owners ADD COLUMN pm_pinned_msg_id INTEGER"
                )
                await self.conn.commit()
            await self._set_version(4)
            version = 4

        if version < SCHEMA_VERSION:
            raise RuntimeError(f"Неизвестная версия схемы БД: {version}")

    async def _migrate_v0_to_v1(self) -> None:
        """Помечает старую схему как версию 1 (создаёт db_version)."""
        await self.conn.execute(
            "CREATE TABLE IF NOT EXISTS db_version ("
            " version INTEGER PRIMARY KEY,"
            " applied_at TEXT NOT NULL DEFAULT '' )"
        )
        await self._set_version(1)

    async def _create_v2_schema(self) -> None:
        """Создаёт multi-tenant схему; легаси-таблицы версии 1 не трогает.

        Таблицы старой схемы назывались chats/users/settings — чтобы не
        конфликтовать с новыми, они переименовываются в *_legacy.
        """
        legacy_rename = {
            "chats": "chats_legacy",
            "users": "users_legacy",
            "settings": "settings_legacy",
        }
        tables = await self._table_names()
        for old, new in legacy_rename.items():
            if old in tables and new not in tables:
                await self.conn.execute(f"ALTER TABLE {old} RENAME TO {new}")

        await self.conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS owners (
                user_id        INTEGER PRIMARY KEY,
                username       TEXT NOT NULL DEFAULT '',
                full_name      TEXT NOT NULL DEFAULT '',
                aternos_session TEXT NOT NULL DEFAULT '',  -- шифрование Fernet
                session_valid  INTEGER NOT NULL DEFAULT 1,
                max_servers    INTEGER NOT NULL DEFAULT 2,
                lockdown_mode  INTEGER NOT NULL DEFAULT 0,
                created_at     TEXT NOT NULL DEFAULT '',
                updated_at     TEXT NOT NULL DEFAULT ''
            );

            CREATE TABLE IF NOT EXISTS servers (
                id            INTEGER PRIMARY KEY AUTOINCREMENT,
                owner_id      INTEGER NOT NULL REFERENCES owners(user_id) ON DELETE CASCADE,
                aternos_id    TEXT NOT NULL DEFAULT '',
                server_ip     TEXT NOT NULL DEFAULT '',
                display_name  TEXT NOT NULL DEFAULT '',
                is_active     INTEGER NOT NULL DEFAULT 1,
                auto_backup_h INTEGER NOT NULL DEFAULT 0,
                auto_confirm  INTEGER NOT NULL DEFAULT 1,
                created_at    TEXT NOT NULL DEFAULT '',
                UNIQUE (owner_id, aternos_id)
            );

            CREATE TABLE IF NOT EXISTS chats (
                chat_id       INTEGER PRIMARY KEY,
                owner_id      INTEGER NOT NULL REFERENCES owners(user_id) ON DELETE CASCADE,
                title         TEXT NOT NULL DEFAULT '',
                pinned_msg_id INTEGER,
                is_active     INTEGER NOT NULL DEFAULT 1,
                created_at    TEXT NOT NULL DEFAULT ''
            );

            CREATE TABLE IF NOT EXISTS chat_servers (
                chat_id   INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
                server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
                PRIMARY KEY (chat_id, server_id)
            );

            CREATE TABLE IF NOT EXISTS users (
                user_id    INTEGER NOT NULL,
                chat_id    INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
                username   TEXT NOT NULL DEFAULT '',
                full_name  TEXT NOT NULL DEFAULT '',
                has_access INTEGER NOT NULL DEFAULT 0,
                created_at TEXT NOT NULL DEFAULT '',
                PRIMARY KEY (user_id, chat_id)
            );
            CREATE INDEX IF NOT EXISTS idx_users_chat ON users (chat_id);

            CREATE TABLE IF NOT EXISTS audit_log (
                id         INTEGER PRIMARY KEY AUTOINCREMENT,
                owner_id   INTEGER NOT NULL,
                user_id    INTEGER,
                chat_id    INTEGER,
                server_id  INTEGER,
                action     TEXT NOT NULL,
                details    TEXT NOT NULL DEFAULT '',
                created_at TEXT NOT NULL DEFAULT ''
            );
            CREATE INDEX IF NOT EXISTS idx_audit_owner ON audit_log (owner_id, id);

            CREATE TABLE IF NOT EXISTS db_version (
                version    INTEGER PRIMARY KEY,
                applied_at TEXT NOT NULL DEFAULT ''
            );
            """
        )
        await self.conn.commit()

    # ------------------------------------------------------------------ #
    # owners
    # ------------------------------------------------------------------ #
    async def create_owner(
        self,
        user_id: int,
        username: str = "",
        full_name: str = "",
        aternos_session: str = "",
        max_servers: int = 2,
    ) -> None:
        """Создаёт владельца с зашифрованной кукой Aternos (idempotent)."""
        now = _now_iso()
        await self.conn.execute(
            "INSERT INTO owners (user_id, username, full_name, aternos_session,"
            " max_servers, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)"
            " ON CONFLICT(user_id) DO UPDATE SET username = excluded.username,"
            " full_name = excluded.full_name, aternos_session = excluded.aternos_session,"
            " session_valid = 1, updated_at = excluded.updated_at",
            (user_id, username, full_name, aternos_session, max_servers, now, now),
        )
        await self.conn.commit()

    async def get_owner(self, user_id: int) -> Optional[dict]:
        cur = await self.conn.execute("SELECT * FROM owners WHERE user_id = ?", (user_id,))
        row = await cur.fetchone()
        return dict(row) if row is not None else None

    async def get_all_owners(self) -> List[dict]:
        """Все владельцы (для суперадмина и фоновых задач)."""
        cur = await self.conn.execute("SELECT * FROM owners ORDER BY created_at")
        return [dict(row) for row in await cur.fetchall()]

    async def is_owner(self, user_id: int) -> bool:
        cur = await self.conn.execute(
            "SELECT 1 FROM owners WHERE user_id = ?", (user_id,)
        )
        return (await cur.fetchone()) is not None

    async def is_chat_owner(self, chat_id: int, user_id: int) -> bool:
        """Является ли пользователь владельцем данного чата."""
        chat = await self.get_chat(chat_id)
        return chat is not None and chat["owner_id"] == user_id

    async def update_owner_session(self, user_id: int, encrypted_session: str) -> None:
        """Обновляет зашифрованную куку и помечает сессию валидной."""
        await self.conn.execute(
            "UPDATE owners SET aternos_session = ?, session_valid = 1, updated_at = ?"
            " WHERE user_id = ?",
            (encrypted_session, _now_iso(), user_id),
        )
        await self.conn.commit()

    async def update_owner_profile(self, user_id: int, username: str, full_name: str) -> None:
        await self.conn.execute(
            "UPDATE owners SET username = ?, full_name = ?, updated_at = ?"
            " WHERE user_id = ?",
            (username, full_name, _now_iso(), user_id),
        )
        await self.conn.commit()

    async def set_owner_lockdown(self, user_id: int, active: bool) -> None:
        await self.conn.execute(
            "UPDATE owners SET lockdown_mode = ?, updated_at = ? WHERE user_id = ?",
            (1 if active else 0, _now_iso(), user_id),
        )
        await self.conn.commit()

    async def get_owner_lockdown(self, user_id: int) -> bool:
        cur = await self.conn.execute(
            "SELECT lockdown_mode FROM owners WHERE user_id = ?", (user_id,)
        )
        row = await cur.fetchone()
        return bool(row["lockdown_mode"]) if row is not None else False

    async def set_owner_pm_pinned(self, user_id: int, msg_id: int) -> None:
        """Сохраняет id закреплённого дашборда владельца в ЛС с ботом."""
        await self.conn.execute(
            "UPDATE owners SET pm_pinned_msg_id = ?, updated_at = ? WHERE user_id = ?",
            (msg_id, _now_iso(), user_id),
        )
        await self.conn.commit()

    async def get_owner_pm_pinned(self, user_id: int) -> Optional[int]:
        """id закреплённого дашборда владельца в ЛС (None — ещё не создан)."""
        cur = await self.conn.execute(
            "SELECT pm_pinned_msg_id FROM owners WHERE user_id = ?", (user_id,)
        )
        row = await cur.fetchone()
        if row is None:
            return None
        return int(row["pm_pinned_msg_id"]) if row["pm_pinned_msg_id"] is not None else None

    async def delete_owner(self, user_id: int) -> None:
        """Удаляет владельца: серверы/чаты/участники удаляются каскадом."""
        await self.conn.execute("DELETE FROM owners WHERE user_id = ?", (user_id,))
        await self.conn.commit()

    # ------------------------------------------------------------------ #
    # servers
    # ------------------------------------------------------------------ #
    async def add_server(
        self,
        owner_id: int,
        aternos_id: str,
        server_ip: str = "",
        display_name: str = "",
        max_servers: Optional[int] = None,
    ) -> Optional[int]:
        """Добавляет сервер владельцу; при переполнении лимита — None."""
        if max_servers is None:
            owner = await self.get_owner(owner_id)
            max_servers = owner["max_servers"] if owner else 0
        count = await self.get_servers_count(owner_id)
        if count >= max_servers:
            return None
        await self.conn.execute(
            "INSERT OR IGNORE INTO servers (owner_id, aternos_id, server_ip,"
            " display_name, created_at) VALUES (?, ?, ?, ?, ?)",
            (owner_id, aternos_id, server_ip, display_name, _now_iso()),
        )
        await self.conn.commit()
        row = await self.get_server_by_aternos_id(owner_id, aternos_id)
        return row["id"] if row else None

    async def get_server(self, server_id: int) -> Optional[dict]:
        cur = await self.conn.execute("SELECT * FROM servers WHERE id = ?", (server_id,))
        row = await cur.fetchone()
        return dict(row) if row is not None else None

    async def get_server_by_aternos_id(self, owner_id: int, aternos_id: str) -> Optional[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM servers WHERE owner_id = ? AND aternos_id = ?",
            (owner_id, aternos_id),
        )
        row = await cur.fetchone()
        return dict(row) if row is not None else None

    async def get_servers_by_owner(self, owner_id: int) -> List[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM servers WHERE owner_id = ? ORDER BY id", (owner_id,)
        )
        return [dict(row) for row in await cur.fetchall()]

    async def get_active_servers_by_owner(self, owner_id: int) -> List[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM servers WHERE owner_id = ? AND is_active = 1 ORDER BY id",
            (owner_id,),
        )
        return [dict(row) for row in await cur.fetchall()]

    async def get_servers_count(self, owner_id: int) -> int:
        cur = await self.conn.execute(
            "SELECT COUNT(*) AS n FROM servers WHERE owner_id = ?", (owner_id,)
        )
        row = await cur.fetchone()
        return int(row["n"]) if row is not None else 0

    async def set_server_active(self, server_id: int, active: bool) -> None:
        await self.conn.execute(
            "UPDATE servers SET is_active = ? WHERE id = ?",
            (1 if active else 0, server_id),
        )
        await self.conn.commit()

    async def deactivate_server(self, server_id: int) -> None:
        """Деактивирует сервер (is_active=0); записи в чатах остаются."""
        await self.conn.execute(
            "UPDATE servers SET is_active = 0 WHERE id = ?", (server_id,)
        )
        await self.conn.commit()

    async def unbind_server_from_all_chats(self, server_id: int) -> None:
        """Отвязывает сервер от ВСЕХ чатов (для деактивации)."""
        await self.conn.execute(
            "DELETE FROM chat_servers WHERE server_id = ?", (server_id,)
        )
        await self.conn.commit()

    async def update_server_name(self, server_id: int, display_name: str) -> None:
        await self.conn.execute(
            "UPDATE servers SET display_name = ? WHERE id = ?", (display_name, server_id)
        )
        await self.conn.commit()

    async def set_server_auto_confirm(self, server_id: int, enabled: bool) -> None:
        await self.conn.execute(
            "UPDATE servers SET auto_confirm = ? WHERE id = ?",
            (1 if enabled else 0, server_id),
        )
        await self.conn.commit()

    async def remove_server(self, server_id: int) -> None:
        """Удаляет сервер; связки чат-сервер удаляются каскадом."""
        await self.conn.execute("DELETE FROM servers WHERE id = ?", (server_id,))
        await self.conn.commit()

    async def set_server_port(self, server_id: int, mc_port: int) -> None:
        """Сохраняет последний известный Minecraft-порт сервера (upsert)."""
        await self.conn.execute(
            "INSERT INTO server_meta (server_id, mc_port, updated_at)"
            " VALUES (?, ?, ?)"
            " ON CONFLICT(server_id) DO UPDATE SET"
            " mc_port = excluded.mc_port, updated_at = excluded.updated_at",
            (server_id, int(mc_port), _now_iso()),
        )
        await self.conn.commit()

    async def get_server_port(self, server_id: int) -> Optional[int]:
        """Последний известный Minecraft-порт сервера (None — неизвестен)."""
        cur = await self.conn.execute(
            "SELECT mc_port FROM server_meta WHERE server_id = ?", (server_id,)
        )
        row = await cur.fetchone()
        return int(row["mc_port"]) if row and row["mc_port"] else None

    # ------------------------------------------------------------------ #
    # chats
    # ------------------------------------------------------------------ #
    async def add_chat(self, chat_id: int, owner_id: int, title: str = "") -> bool:
        """Привязывает чат к владельцу. False, если чат уже занят другим."""
        row = await self.get_chat(chat_id)
        if row is not None:
            return row["owner_id"] == owner_id
        await self.conn.execute(
            "INSERT OR IGNORE INTO chats (chat_id, owner_id, title, created_at)"
            " VALUES (?, ?, ?, ?)",
            (chat_id, owner_id, title, _now_iso()),
        )
        await self.conn.commit()
        return True

    async def get_chat(self, chat_id: int) -> Optional[dict]:
        cur = await self.conn.execute("SELECT * FROM chats WHERE chat_id = ?", (chat_id,))
        row = await cur.fetchone()
        return dict(row) if row is not None else None

    async def get_chat_owner(self, chat_id: int) -> Optional[dict]:
        """Строка владельца (owners) привязанного чата; None — чат не привязан."""
        cur = await self.conn.execute(
            "SELECT o.* FROM chats c JOIN owners o ON o.user_id = c.owner_id "
            "WHERE c.chat_id = ?",
            (chat_id,),
        )
        row = await cur.fetchone()
        return dict(row) if row is not None else None

    async def chat_exists(self, chat_id: int) -> bool:
        cur = await self.conn.execute("SELECT 1 FROM chats WHERE chat_id = ?", (chat_id,))
        return (await cur.fetchone()) is not None

    async def get_chats_by_owner(self, owner_id: int) -> List[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM chats WHERE owner_id = ? AND is_active = 1 ORDER BY chat_id",
            (owner_id,),
        )
        return [dict(row) for row in await cur.fetchall()]

    async def set_chat_title(self, chat_id: int, title: str) -> None:
        await self.conn.execute("UPDATE chats SET title = ? WHERE chat_id = ?", (title, chat_id))
        await self.conn.commit()

    async def set_chat_pinned_msg(self, chat_id: int, msg_id: int) -> None:
        await self.conn.execute(
            "UPDATE chats SET pinned_msg_id = ? WHERE chat_id = ?", (msg_id, chat_id)
        )
        await self.conn.commit()

    async def get_chat_pinned_msg(self, chat_id: int) -> Optional[int]:
        cur = await self.conn.execute(
            "SELECT pinned_msg_id FROM chats WHERE chat_id = ?", (chat_id,)
        )
        row = await cur.fetchone()
        if row is None:
            return None
        return int(row["pinned_msg_id"]) if row["pinned_msg_id"] is not None else None

    async def set_chat_active(self, chat_id: int, active: bool) -> None:
        await self.conn.execute(
            "UPDATE chats SET is_active = ? WHERE chat_id = ?",
            (1 if active else 0, chat_id),
        )
        await self.conn.commit()

    async def remove_chat(self, chat_id: int) -> None:
        """Отвязывает чат; chat_servers и users удаляются каскадом."""
        await self.conn.execute("DELETE FROM chats WHERE chat_id = ?", (chat_id,))
        await self.conn.commit()

    # ------------------------------------------------------------------ #
    # chat_servers
    # ------------------------------------------------------------------ #
    async def link_server_to_chat(self, chat_id: int, server_id: int) -> None:
        await self.conn.execute(
            "INSERT OR IGNORE INTO chat_servers (chat_id, server_id) VALUES (?, ?)",
            (chat_id, server_id),
        )
        await self.conn.commit()

    async def unlink_server_from_chat(self, chat_id: int, server_id: int) -> None:
        await self.conn.execute(
            "DELETE FROM chat_servers WHERE chat_id = ? AND server_id = ?",
            (chat_id, server_id),
        )
        await self.conn.commit()

    async def get_chat_server_ids(self, chat_id: int) -> List[int]:
        cur = await self.conn.execute(
            "SELECT server_id FROM chat_servers WHERE chat_id = ?", (chat_id,)
        )
        return [int(row["server_id"]) for row in await cur.fetchall()]

    async def get_chat_servers(self, chat_id: int) -> List[dict]:
        """Серверы, привязанные к чату (только активные, с деталями)."""
        cur = await self.conn.execute(
            "SELECT s.* FROM servers s JOIN chat_servers cs ON cs.server_id = s.id"
            " WHERE cs.chat_id = ? AND s.is_active = 1 ORDER BY s.id",
            (chat_id,),
        )
        return [dict(row) for row in await cur.fetchall()]

    async def get_chats_for_server(self, server_id: int) -> List[dict]:
        """Чаты, где привязан сервер (для рассылок и обновления дашбордов)."""
        cur = await self.conn.execute(
            "SELECT c.* FROM chats c JOIN chat_servers cs ON cs.chat_id = c.chat_id"
            " WHERE cs.server_id = ? AND c.is_active = 1",
            (server_id,),
        )
        return [dict(row) for row in await cur.fetchall()]

    async def is_server_linked_to_chat(self, chat_id: int, server_id: int) -> bool:
        cur = await self.conn.execute(
            "SELECT 1 FROM chat_servers WHERE chat_id = ? AND server_id = ?",
            (chat_id, server_id),
        )
        return (await cur.fetchone()) is not None

    # ------------------------------------------------------------------ #
    # users (участники чатов)
    # ------------------------------------------------------------------ #
    async def upsert_chat_user(
        self, chat_id: int, user_id: int, username: str = "", full_name: str = ""
    ) -> None:
        """Регистрирует участника чата; права (has_access) сохраняются."""
        await self.conn.execute(
            "INSERT INTO users (user_id, chat_id, username, full_name, created_at)"
            " VALUES (?, ?, ?, ?, ?)"
            " ON CONFLICT(user_id, chat_id) DO UPDATE SET"
            " username = excluded.username, full_name = excluded.full_name",
            (user_id, chat_id, username, full_name, _now_iso()),
        )
        await self.conn.commit()

    async def get_user_access(self, user_id: int, chat_id: int) -> bool:
        cur = await self.conn.execute(
            "SELECT has_access FROM users WHERE user_id = ? AND chat_id = ?",
            (user_id, chat_id),
        )
        row = await cur.fetchone()
        return bool(row["has_access"]) if row is not None else False

    async def set_user_access(self, user_id: int, chat_id: int, has_access: bool) -> None:
        await self.conn.execute(
            "INSERT INTO users (user_id, chat_id, created_at) VALUES (?, ?, ?)"
            " ON CONFLICT(user_id, chat_id) DO NOTHING",
            (user_id, chat_id, _now_iso()),
        )
        await self.conn.execute(
            "UPDATE users SET has_access = ? WHERE user_id = ? AND chat_id = ?",
            (1 if has_access else 0, user_id, chat_id),
        )
        await self.conn.commit()

    async def user_can_manage(self, user_id: int, chat_id: int) -> bool:
        """Может ли пользователь управлять серверами в этом чате.

        Владелец чата — всегда True (включая lockdown). Для остальных —
        has_access = 1 и lockdown выключен.
        """
        chat = await self.get_chat(chat_id)
        if chat is None:
            return False
        if chat["owner_id"] == user_id:
            return True
        if await self.get_owner_lockdown(chat["owner_id"]):
            return False
        return await self.get_user_access(user_id, chat_id)

    async def get_chat_users_paginated(
        self, chat_id: int, limit: int, offset: int
    ) -> List[dict]:
        """Страница участников чата (доступные первыми, затем по имени)."""
        cur = await self.conn.execute(
            "SELECT * FROM users WHERE chat_id = ?"
            " ORDER BY has_access DESC, full_name COLLATE NOCASE, user_id"
            " LIMIT ? OFFSET ?",
            (chat_id, limit, max(offset, 0)),
        )
        return [dict(row) for row in await cur.fetchall()]

    async def get_chat_users_count(self, chat_id: int) -> int:
        cur = await self.conn.execute(
            "SELECT COUNT(*) AS n FROM users WHERE chat_id = ?", (chat_id,)
        )
        row = await cur.fetchone()
        return int(row["n"]) if row is not None else 0

    # ------------------------------------------------------------------ #
    # audit_log
    # ------------------------------------------------------------------ #
    async def log_action(
        self,
        owner_id: int,
        action: str,
        details: str = "",
        user_id: Optional[int] = None,
        chat_id: Optional[int] = None,
        server_id: Optional[int] = None,
    ) -> None:
        """Пишет запись в журнал действий владельца."""
        await self.conn.execute(
            "INSERT INTO audit_log (owner_id, user_id, chat_id, server_id,"
            " action, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
            (owner_id, user_id, chat_id, server_id, action, details, _now_iso()),
        )
        await self.conn.commit()

    async def get_audit_log(self, owner_id: int, limit: int = 20) -> List[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM audit_log WHERE owner_id = ? ORDER BY id DESC LIMIT ?",
            (owner_id, limit),
        )
        return [dict(row) for row in await cur.fetchall()]


# ---------------------------------------------------------------------- #
# Функциональный слой: `await database.add_chat(...)` и т.д.
# Работает через глобальный экземпляр, создаваемый лениво при первом вызове.
# ---------------------------------------------------------------------- #
_db: Optional[Database] = None


def get_db() -> Database:
    """Возвращает глобальный экземпляр Database (ленивая инициализация)."""
    global _db
    if _db is None:
        _db = Database()
    return _db


async def init_db() -> None:
    """Открывает БД и приводит схему к актуальной версии (вызывается на старте)."""
    await get_db().init_db()


async def close_db() -> None:
    """Закрывает соединение с базой при завершении бота."""
    await get_db().close()


# --- owners ---
async def create_owner(user_id: int, username: str = "", full_name: str = "", aternos_session: str = "", max_servers: int = 2) -> None:
    """Создаёт владельца с зашифрованной кукой (обёртка)."""
    await get_db().create_owner(user_id, username, full_name, aternos_session, max_servers)


async def get_owner(user_id: int) -> Optional[dict]:
    """Владелец по ID или None (обёртка)."""
    return await get_db().get_owner(user_id)


async def get_all_owners() -> List[dict]:
    """Все владельцы (обёртка)."""
    return await get_db().get_all_owners()


async def is_owner(user_id: int) -> bool:
    """Является ли пользователь владельцем (обёртка)."""
    return await get_db().is_owner(user_id)


async def is_chat_owner(chat_id: int, user_id: int) -> bool:
    """Является ли пользователь владельцем чата (обёртка)."""
    return await get_db().is_chat_owner(chat_id, user_id)


async def set_owner_pm_pinned(user_id: int, msg_id: int) -> None:
    """Сохраняет id закреплённого дашборда владельца в ЛС (обёртка)."""
    await get_db().set_owner_pm_pinned(user_id, msg_id)


async def get_owner_pm_pinned(user_id: int) -> Optional[int]:
    """id закреплённого дашборда владельца в ЛС (обёртка)."""
    return await get_db().get_owner_pm_pinned(user_id)


async def delete_owner(user_id: int) -> None:
    """Удаляет владельца со всеми серверами и чатами (обёртка)."""
    await get_db().delete_owner(user_id)


async def update_owner_session(user_id: int, encrypted_session: str) -> None:
    """Обновляет зашифрованную куку (обёртка)."""
    await get_db().update_owner_session(user_id, encrypted_session)


async def update_owner_profile(user_id: int, username: str, full_name: str) -> None:
    """Обновляет профиль владельца (обёртка)."""
    await get_db().update_owner_profile(user_id, username, full_name)


async def set_owner_lockdown(user_id: int, active: bool) -> None:
    """Включает/выключает lockdown владельца (обёртка)."""
    await get_db().set_owner_lockdown(user_id, active)


async def get_owner_lockdown(user_id: int) -> bool:
    """Текущее состояние lockdown владельца (обёртка)."""
    return await get_db().get_owner_lockdown(user_id)


# --- servers ---
async def add_server(owner_id: int, aternos_id: str, server_ip: str = "", display_name: str = "") -> Optional[int]:
    """Добавляет сервер владельцу; None при переполнении лимита (обёртка)."""
    return await get_db().add_server(owner_id, aternos_id, server_ip, display_name)


async def get_server(server_id: int) -> Optional[dict]:
    """Сервер по ID (обёртка)."""
    return await get_db().get_server(server_id)


async def get_server_by_aternos_id(owner_id: int, aternos_id: str) -> Optional[dict]:
    """Сервер владельца по aternos_id (обёртка)."""
    return await get_db().get_server_by_aternos_id(owner_id, aternos_id)


async def get_servers_by_owner(owner_id: int) -> List[dict]:
    """Все серверы владельца (обёртка)."""
    return await get_db().get_servers_by_owner(owner_id)


async def get_active_servers_by_owner(owner_id: int) -> List[dict]:
    """Активные серверы владельца (обёртка)."""
    return await get_db().get_active_servers_by_owner(owner_id)


async def set_server_active(server_id: int, active: bool) -> None:
    """Включает/выключает сервер (обёртка)."""
    await get_db().set_server_active(server_id, active)


async def deactivate_server(server_id: int) -> None:
    """Деактивирует сервер (обёртка)."""
    await get_db().deactivate_server(server_id)


async def unbind_server_from_all_chats(server_id: int) -> None:
    """Отвязывает сервер от всех чатов (обёртка)."""
    await get_db().unbind_server_from_all_chats(server_id)


async def update_server_name(server_id: int, display_name: str) -> None:
    """Переименовывает сервер (обёртка)."""
    await get_db().update_server_name(server_id, display_name)


async def set_server_auto_confirm(server_id: int, enabled: bool) -> None:
    """Задаёт автоподтверждение запуска (обёртка)."""
    await get_db().set_server_auto_confirm(server_id, enabled)


async def remove_server(server_id: int) -> None:
    """Удаляет сервер (обёртка)."""
    await get_db().remove_server(server_id)


async def set_server_port(server_id: int, mc_port: int) -> None:
    """Сохраняет последний известный Minecraft-порт сервера (обёртка)."""
    await get_db().set_server_port(server_id, mc_port)


async def get_server_port(server_id: int) -> Optional[int]:
    """Последний известный Minecraft-порт сервера (обёртка)."""
    return await get_db().get_server_port(server_id)


# --- chats ---
async def add_chat(chat_id: int, owner_id: int, title: str = "") -> bool:
    """Привязывает чат к владельцу; False, если чат занят другим (обёртка)."""
    return await get_db().add_chat(chat_id, owner_id, title)


async def get_chat(chat_id: int) -> Optional[dict]:
    """Чат по id (обёртка)."""
    return await get_db().get_chat(chat_id)


async def get_chat_owner(chat_id: int) -> Optional[dict]:
    """Строка владельца привязанного чата (обёртка)."""
    return await get_db().get_chat_owner(chat_id)


async def chat_exists(chat_id: int) -> bool:
    """Зарегистрирован ли чат (обёртка)."""
    return await get_db().chat_exists(chat_id)


async def get_chats_by_owner(owner_id: int) -> List[dict]:
    """Активные чаты владельца (обёртка)."""
    return await get_db().get_chats_by_owner(owner_id)


async def set_chat_title(chat_id: int, title: str) -> None:
    """Обновляет название чата (обёртка)."""
    await get_db().set_chat_title(chat_id, title)


async def set_chat_pinned_msg(chat_id: int, msg_id: int) -> None:
    """Сохраняет id закреплённого дашборда (обёртка)."""
    await get_db().set_chat_pinned_msg(chat_id, msg_id)


async def get_chat_pinned_msg(chat_id: int) -> Optional[int]:
    """ID закреплённого дашборда чата (обёртка)."""
    return await get_db().get_chat_pinned_msg(chat_id)


async def set_chat_active(chat_id: int, active: bool) -> None:
    """Включает/выключает чат (обёртка)."""
    await get_db().set_chat_active(chat_id, active)


async def remove_chat(chat_id: int) -> None:
    """Отвязывает чат; связки и юзеры удаляются каскадом (обёртка)."""
    await get_db().remove_chat(chat_id)


# --- chat_servers ---
async def link_server_to_chat(chat_id: int, server_id: int) -> None:
    """Привязывает сервер к чату (обёртка)."""
    await get_db().link_server_to_chat(chat_id, server_id)


async def unlink_server_from_chat(chat_id: int, server_id: int) -> None:
    """Отвязывает сервер от чата (обёртка)."""
    await get_db().unlink_server_from_chat(chat_id, server_id)


async def get_chat_server_ids(chat_id: int) -> List[int]:
    """ID серверов, привязанных к чату (обёртка)."""
    return await get_db().get_chat_server_ids(chat_id)


async def get_chat_servers(chat_id: int) -> List[dict]:
    """Активные серверы чата с деталями (обёртка)."""
    return await get_db().get_chat_servers(chat_id)


async def get_chats_for_server(server_id: int) -> List[dict]:
    """Чаты, где привязан сервер (обёртка)."""
    return await get_db().get_chats_for_server(server_id)


async def is_server_linked_to_chat(chat_id: int, server_id: int) -> bool:
    """Привязан ли сервер к чату (обёртка)."""
    return await get_db().is_server_linked_to_chat(chat_id, server_id)


# --- users ---
async def upsert_chat_user(chat_id: int, user_id: int, username: str = "", full_name: str = "") -> None:
    """Регистрирует участника чата (обёртка)."""
    await get_db().upsert_chat_user(chat_id, user_id, username, full_name)


async def get_user_access(user_id: int, chat_id: int) -> bool:
    """Право доступа пользователя в чате (обёртка)."""
    return await get_db().get_user_access(user_id, chat_id)


async def set_user_access(user_id: int, chat_id: int, has_access: bool) -> None:
    """Выдаёт/отзывает доступ в чате (обёртка)."""
    await get_db().set_user_access(user_id, chat_id, has_access)


async def user_can_manage(user_id: int, chat_id: int) -> bool:
    """Может ли пользователь управлять серверами в чате (обёртка)."""
    return await get_db().user_can_manage(user_id, chat_id)


async def get_chat_users_paginated(chat_id: int, limit: int, offset: int) -> List[dict]:
    """Страница участников чата (обёртка)."""
    return await get_db().get_chat_users_paginated(chat_id, limit, offset)


async def get_chat_users_count(chat_id: int) -> int:
    """Число участников чата (обёртка)."""
    return await get_db().get_chat_users_count(chat_id)


# --- audit_log ---
async def log_action(
    owner_id: int,
    action: str,
    details: str = "",
    user_id: Optional[int] = None,
    chat_id: Optional[int] = None,
    server_id: Optional[int] = None,
) -> None:
    """Пишет запись в журнал (обёртка)."""
    await get_db().log_action(owner_id, action, details, user_id, chat_id, server_id)


async def get_audit_log(owner_id: int, limit: int = 20) -> List[dict]:
    """Последние записи журнала владельца (обёртка)."""
    return await get_db().get_audit_log(owner_id, limit)
