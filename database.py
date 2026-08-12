"""MineOps - async SQLite storage. Every query is parameterized."""

from __future__ import annotations

import os
from typing import Any, List, Optional

import aiosqlite

DB_PATH = os.getenv("DB_PATH", "data/bot_data.db")


class Database:
    def __init__(self, db_path: str = DB_PATH, owner_id: int = 0) -> None:
        self.db_path = db_path
        self.owner_id = int(owner_id or 0)
        self._conn: Optional[aiosqlite.Connection] = None

    @property
    def conn(self) -> aiosqlite.Connection:
        if self._conn is None:
            raise RuntimeError("call Database.init() before use")
        return self._conn

    async def init(self) -> None:
        dirname = os.path.dirname(self.db_path)
        if dirname:
            os.makedirs(dirname, exist_ok=True)
        self._conn = await aiosqlite.connect(self.db_path)
        self._conn.row_factory = aiosqlite.Row
        await self._conn.execute("PRAGMA journal_mode = WAL")
        await self._conn.execute("PRAGMA busy_timeout = 5000")
        await self._conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS chats (
                chat_id INTEGER PRIMARY KEY,
                title   TEXT NOT NULL DEFAULT '',
                type    TEXT NOT NULL DEFAULT 'private'
            );
            CREATE TABLE IF NOT EXISTS users (
                chat_id    INTEGER NOT NULL,
                user_id    INTEGER NOT NULL,
                username   TEXT    NOT NULL DEFAULT '',
                first_name TEXT    NOT NULL DEFAULT '',
                has_access INTEGER NOT NULL DEFAULT 0,
                PRIMARY KEY (chat_id, user_id)
            );
            CREATE INDEX IF NOT EXISTS idx_users_chat ON users (chat_id);
            CREATE TABLE IF NOT EXISTS settings (
                key   TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );
            """
        )
        await self._conn.commit()

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    @staticmethod
    def _row(row: Optional[aiosqlite.Row]) -> Optional[dict]:
        return dict(row) if row is not None else None

    # --- chats ---
    async def upsert_chat(self, chat_id: int, title: str = "", chat_type: str = "private") -> None:
        await self.conn.execute(
            "INSERT INTO chats (chat_id, title, type) VALUES (?, ?, ?) "
            "ON CONFLICT(chat_id) DO UPDATE SET title = excluded.title, type = excluded.type",
            (chat_id, title, chat_type),
        )
        await self.conn.commit()

    async def get_chat(self, chat_id: int) -> Optional[dict]:
        cur = await self.conn.execute("SELECT * FROM chats WHERE chat_id = ?", (chat_id,))
        return self._row(await cur.fetchone())

    async def list_chats(self) -> List[dict]:
        cur = await self.conn.execute("SELECT * FROM chats ORDER BY chat_id")
        return [dict(r) for r in await cur.fetchall()]

    # --- users (RBAC) ---
    async def upsert_user(self, chat_id: int, user_id: int, username: str = "", first_name: str = "") -> None:
        # has_access is preserved on conflict so granted users keep their access.
        await self.conn.execute(
            "INSERT INTO users (chat_id, user_id, username, first_name) VALUES (?, ?, ?, ?) "
            "ON CONFLICT(chat_id, user_id) DO UPDATE SET "
            "username = excluded.username, first_name = excluded.first_name",
            (chat_id, user_id, username, first_name),
        )
        await self.conn.commit()

    async def get_user(self, chat_id: int, user_id: int) -> Optional[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM users WHERE chat_id = ? AND user_id = ?", (chat_id, user_id)
        )
        return self._row(await cur.fetchone())

    async def has_access(self, chat_id: int, user_id: int) -> bool:
        # The owner can NEVER lose access.
        if user_id == self.owner_id:
            return True
        row = await self.get_user(chat_id, user_id)
        return bool(row and row["has_access"])

    async def list_users(self, chat_id: int) -> List[dict]:
        cur = await self.conn.execute(
            "SELECT * FROM users WHERE chat_id = ? "
            "ORDER BY has_access DESC, username COLLATE NOCASE, user_id",
            (chat_id,),
        )
        return [dict(r) for r in await cur.fetchall()]

    async def set_access(self, chat_id: int, user_id: int, has_access: bool) -> None:
        # The owner's access is permanent: revoking it is a no-op, and the
        # owner always has a row with has_access = 1 so they appear in lists.
        if user_id == self.owner_id:
            if not has_access:
                return
            await self.conn.execute(
                "INSERT INTO users (chat_id, user_id, has_access) VALUES (?, ?, 1) "
                "ON CONFLICT(chat_id, user_id) DO UPDATE SET has_access = 1",
                (chat_id, user_id),
            )
            await self.conn.commit()
            return
        await self.conn.execute(
            "UPDATE users SET has_access = ? WHERE chat_id = ? AND user_id = ?",
            (1 if has_access else 0, chat_id, user_id),
        )
        await self.conn.commit()

    async def revoke_all_except_owner(self, owner_id: int) -> int:
        # Emergency revoke: explicitly excludes the owner, so the owner can
        # never be locked out, even if own access was somehow set.
        cur = await self.conn.execute(
            "UPDATE users SET has_access = 0 WHERE user_id != ? AND has_access = 1",
            (owner_id,),
        )
        await self.conn.commit()
        return cur.rowcount or 0

    # --- settings ---
    async def set_setting(self, key: str, value: Any) -> None:
        await self.conn.execute(
            "INSERT INTO settings (key, value) VALUES (?, ?) "
            "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            (key, str(value)),
        )
        await self.conn.commit()

    async def get_setting(self, key: str) -> Optional[str]:
        cur = await self.conn.execute("SELECT value FROM settings WHERE key = ?", (key,))
        row = await cur.fetchone()
        return str(row["value"]) if row else None

    async def unset_setting(self, key: str) -> None:
        await self.conn.execute("DELETE FROM settings WHERE key = ?", (key,))
        await self.conn.commit()

    async def set_pinned_message(self, message_id: int) -> None:
        await self.set_setting("pinned_message_id", message_id)

    async def get_pinned_message(self) -> Optional[int]:
        raw = await self.get_setting("pinned_message_id")
        try:
            return int(raw) if raw is not None else None
        except ValueError:
            return None

    async def unset_pinned_message(self) -> None:
        await self.unset_setting("pinned_message_id")

    async def set_backup_hours(self, hours: float) -> None:
        await self.set_setting("backup_hours", hours)

    async def get_backup_hours(self, default: float = 24.0) -> float:
        raw = await self.get_setting("backup_hours")
        try:
            return float(raw) if raw is not None else default
        except ValueError:
            return default