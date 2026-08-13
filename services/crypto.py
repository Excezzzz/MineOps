"""Шифрование кук Aternos (Fernet / cryptography).

Ключ берётся из ENCRYPTION_KEY в .env (если задан), иначе генерируется
при первом запуске и сохраняется в data/secret.key. Один и тот же ключ
используется для всех владельцев: куки шифруются перед записью в БД и
расшифровываются при чтении.
"""

from __future__ import annotations

import os
from pathlib import Path

from cryptography.fernet import Fernet, InvalidToken

from config import config

KEY_FILE = Path("data/secret.key")


def _load_or_create_key() -> Fernet:
    """Возвращает Fernet по ключу из .env или из data/secret.key (автогенерация)."""
    env_key = (config.ENCRYPTION_KEY or "").strip()
    if env_key:
        try:
            return Fernet(env_key.encode())
        except (ValueError, TypeError):
            pass  # некорректный ключ в .env — переходим к файловому ключу

    if KEY_FILE.exists():
        raw = KEY_FILE.read_text(encoding="utf-8").strip()
        return Fernet(raw.encode())

    key = Fernet.generate_key()
    os.makedirs(KEY_FILE.parent, exist_ok=True)
    KEY_FILE.write_text(key.decode(), encoding="utf-8")
    try:
        os.chmod(KEY_FILE, 0o600)
    except OSError:
        pass  # Windows не поддерживает chmod — не критично
    return Fernet(key)


_fernet = _load_or_create_key()


def encrypt_session(plain: str) -> str:
    """Шифрует куку Aternos (пустая строка остаётся пустой)."""
    if not plain:
        return ""
    return _fernet.encrypt(plain.encode("utf-8")).decode("ascii")


def decrypt_session(encrypted: str) -> str:
    """Расшифровывает куку Aternos; повреждённые данные возвращают пустую строку."""
    if not encrypted:
        return ""
    try:
        return _fernet.decrypt(encrypted.encode("ascii")).decode("utf-8")
    except (InvalidToken, ValueError):
        return ""
