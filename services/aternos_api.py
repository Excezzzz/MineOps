"""Работа с Aternos через python-aternos (multi-tenant, per-owner).

Архитектурные правила:
  * python-aternos — СИНХРОННАЯ библиотека, все вызовы обёрнуты в
    asyncio.to_thread() и сериализованы per-owner блокировкой;
  * python-aternos используется ТОЛЬКО для действий (start, stop,
    confirm, list_servers) и проверки очереди запуска — НИКОГДА для
    регулярного polling статуса (это делает mcsrvstat.us);
  * клиент создаётся по owner_id: кука расшифровывается из БД (Fernet),
    сам клиент кешируется на 30 минут (dict[int, (Client, expires_at)]).

Любые ошибки библиотеки оборачиваются в понятный AternosError.
"""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Any, Dict, List, Tuple

import database
from services.crypto import decrypt_session

try:
    from python_aternos import AternosServer
except ImportError:
    from python_aternos.atserver import AternosServer
from python_aternos import Client

logger = logging.getLogger(__name__)

CLIENT_TTL_SECONDS = 30 * 60  # TTL кеша клиентов: 30 минут


class AternosError(Exception):
    """Ошибка взаимодействия с Aternos (обёртка над ошибками библиотеки)."""


class AternosManager:
    """Синхронную логику python-aternos скрывает за асинхронным per-owner API."""

    # Кеш клиентов: owner_id -> (Client, время создания). Ключ инвалидируется
    # при обновлении куки (update_session) и истечении TTL.
    _client_cache: Dict[int, Tuple[Client, float]] = {}
    # Блокировки per-owner: последовательные обращения к Aternos в рамках
    # одного аккаунта не пересекаются (защита от Cloudflare-бана).
    _locks: Dict[int, asyncio.Lock] = {}

    def __init__(self, owner_id: int) -> None:
        self.owner_id = owner_id

    # ------------------------------------------------------------------ #
    # внутренняя синхронная часть (вызывается ТОЛЬКО внутри to_thread)
    # ------------------------------------------------------------------ #
    def _get_client_sync(self, cookie: str) -> Client:
        """Создаёт/возвращает закешированный клиент для owner_id."""
        cached = self._client_cache.get(self.owner_id)
        if cached and time.monotonic() - cached[1] < CLIENT_TTL_SECONDS:
            return cached[0]

        client = Client()
        try:
            client.login_with_session(cookie)
        except Exception as e:
            logger.error("owner %s: логин по куке не удался: %s", self.owner_id, e)
            raise AternosError(
                "⚠️ Сессия Aternos истекла или недействительна!\n"
                "Обновите куку командой /set_session в личке с ботом."
            ) from e

        self._client_cache[self.owner_id] = (client, time.monotonic())
        return client

    def _get_server_sync(self, client: Client, aternos_id: str) -> AternosServer:
        atconn = getattr(client, "atconn", client)
        return AternosServer(aternos_id, atconn)

    def _list_servers_sync(self, cookie: str) -> List[dict]:
        """Возвращает серверы аккаунта: aternos_id, server_ip, display_name.

        В python-aternos 3.0.6 список серверов живёт на AternosAccount
        (client.account.list_servers), а не на самом Client. Домен и имя
        сервера доступны только после fetch() (парсинг lastStatus с панели);
        при неудаче fetch — aternos_id остаётся, а адрес пустой.
        """
        client = self._get_client_sync(cookie)
        servers: List[dict] = []
        account = getattr(client, "account", None)
        try:
            raw = account.list_servers() if account is not None else []
        except Exception as e:
            logger.warning("owner %s: не удалось получить список серверов: %s", self.owner_id, e)
            return servers
        for s in raw:
            aternos_id = str(getattr(s, "servid", "") or "")
            if not aternos_id:
                continue
            server_ip = ""
            name = ""
            try:
                s.fetch()
                info = getattr(s, "_info", {}) or {}
                server_ip = str(info.get("ip", "") or "")
                name = str(info.get("name", "") or "")
            except Exception as e:
                logger.warning(
                    "owner %s: не удалось прочитать инфо сервера %s: %s",
                    self.owner_id, aternos_id, e,
                )
            if not server_ip:
                try:
                    subdomain = str(getattr(s, "subdomain", "") or "")
                except Exception:
                    subdomain = ""
                if subdomain and not subdomain.endswith(".aternos.me"):
                    subdomain = f"{subdomain}.aternos.me"
                server_ip = subdomain
            servers.append(
                {
                    "aternos_id": aternos_id,
                    "server_ip": server_ip,
                    "display_name": name or server_ip or aternos_id,
                }
            )
        return servers

    def _start_sync(self, cookie: str, aternos_id: str) -> None:
        client = self._get_client_sync(cookie)
        server = self._get_server_sync(client, aternos_id)
        try:
            if hasattr(server, "eula"):
                server.eula()
        except Exception as e:
            logger.warning("owner %s: EULA check не выполнен: %s", self.owner_id, e)
        server.start()
        logger.info("owner %s: команда start отправлена (aternos_id=%s)", self.owner_id, aternos_id)

    def _stop_sync(self, cookie: str, aternos_id: str) -> None:
        client = self._get_client_sync(cookie)
        server = self._get_server_sync(client, aternos_id)
        server.stop()
        logger.info("owner %s: команда stop отправлена (aternos_id=%s)", self.owner_id, aternos_id)

    def _queue_status_sync(self, cookie: str, aternos_id: str) -> dict:
        """Состояние сервера с панели (lastStatus -> _info) для queue_watcher.

        fetch() дёргает панель Aternos (1 запрос). Ошибки НЕ глотаем —
        транзиентные Cloudflare-блоки учитываются счётчиком в watcher'е.
        """
        client = self._get_client_sync(cookie)
        server = self._get_server_sync(client, aternos_id)
        server.fetch()
        return dict(server._info or {})

    def _panel_status_sync(self, cookie: str, aternos_id: str) -> dict:
        """Статус сервера с панели Aternos: online/offline, игроки, порт.

        Панель — авторитетный источник: её status (0=оффлайн, 1=онлайн),
        players/slots и playerlist (реальные ники). Публичный legacy-пинг
        на Aternos ВРЁТ: прокси отвечает даже на оффлайн-серверах.
        """
        client = self._get_client_sync(cookie)
        server = self._get_server_sync(client, aternos_id)
        server.fetch()
        info = dict(server._info or {})
        return {
            "panel_status": int(info.get("status") or 0),
            "players": int(info.get("players") or 0),
            "slots": int(info.get("slots") or 0),
            "port": int(info.get("port") or 0),
            "playerlist": [str(n) for n in (info.get("playerlist") or [])],
        }

    def _confirm_sync(self, cookie: str, aternos_id: str) -> None:
        client = self._get_client_sync(cookie)
        server = self._get_server_sync(client, aternos_id)
        server.confirm()
        logger.info("owner %s: сервер %s подтверждён", self.owner_id, aternos_id)

    # ------------------------------------------------------------------ #
    # асинхронный публичный API
    # ------------------------------------------------------------------ #
    async def _cookie(self) -> str:
        """Расшифрованная кука владельца (None -> AternosError)."""
        owner = await database.get_owner(self.owner_id)
        if owner is None:
            raise AternosError("Владелец не найден: пройдите онбординг заново.")
        cookie = decrypt_session(owner["aternos_session"] or "")
        if not cookie:
            raise AternosError("Сессия Aternos не настроена: выполните /set_session.")
        return cookie

    async def _run(self, action_sync, *args) -> Any:
        """Выполняет синхронное действие в потоке под блокировкой владельца."""
        cookie = await self._cookie()
        lock = self._locks.setdefault(self.owner_id, asyncio.Lock())
        async with lock:
            try:
                return await asyncio.to_thread(action_sync, cookie, *args)
            except AternosError:
                raise
            except Exception as exc:
                logger.exception("owner %s: ошибка python-aternos: %s", self.owner_id, exc)
                if "Cloudflare" in str(exc):
                    raise AternosError(
                        "⚠️ Aternos временно заблокировал запрос (Cloudflare). "
                        "Подождите 3-5 минут или обновите куку /set_session."
                    ) from exc
                raise AternosError(f"Не удалось выполнить запрос к Aternos: {exc}") from exc

    def _check_session_sync(self, cookie: str) -> bool:
        """Проверяет куку: вход в аккаунт без обращения к серверам.

        Используется фоновой проверкой: дешевле, чем list_servers.
        """
        client = Client()
        client.login_with_session(cookie)
        return True

    async def check_session(self) -> None:
        """Проверяет куку владельца из БД; AternosError если просрочена."""
        await self._run(self._check_session_sync)

    async def list_account_servers(self) -> List[dict]:
        """Серверы аккаунта владельца (для онбординга и панели)."""
        return await self._run(self._list_servers_sync)

    async def start_server(self, server_id: int) -> str:
        """Запускает сервер владельца по его id в БД."""
        server = await database.get_server(server_id)
        if server is None or server["owner_id"] != self.owner_id:
            raise AternosError("Сервер не найден или не принадлежит владельцу.")
        await self._run(self._start_sync, server["aternos_id"])
        await database.log_action(
            self.owner_id, "server_start", server["display_name"], server_id=server_id
        )
        return f"Запуск сервера {server['display_name']} запрошен."

    async def stop_server(self, server_id: int) -> str:
        """Останавливает сервер владельца по его id в БД."""
        server = await database.get_server(server_id)
        if server is None or server["owner_id"] != self.owner_id:
            raise AternosError("Сервер не найден или не принадлежит владельцу.")
        await self._run(self._stop_sync, server["aternos_id"])
        await database.log_action(
            self.owner_id, "server_stop", server["display_name"], server_id=server_id
        )
        return f"Остановка сервера {server['display_name']} запрошена."

    async def get_queue_status(self, server_id: int) -> dict:
        """Текущий статус очереди запуска сервера (для queue_watcher)."""
        server = await database.get_server(server_id)
        if server is None or server["owner_id"] != self.owner_id:
            raise AternosError("Сервер не найден или не принадлежит владельцу.")
        return await self._run(self._queue_status_sync, server["aternos_id"])

    async def get_panel_status(self, server_id: int) -> dict:
        """Статус сервера с панели Aternos (авторитетный источник).

        Возвращает {panel_status, players, slots, port, playerlist};
        panel_status: 0 = оффлайн, 1 = онлайн.
        """
        server = await database.get_server(server_id)
        if server is None or server["owner_id"] != self.owner_id:
            raise AternosError("Сервер не найден или не принадлежит владельцу.")
        return await self._run(self._panel_status_sync, server["aternos_id"])

    async def confirm_server(self, server_id: int) -> None:
        """Подтверждает запуск сервера, дошедшего до очереди (auto_confirm)."""
        server = await database.get_server(server_id)
        if server is None or server["owner_id"] != self.owner_id:
            raise AternosError("Сервер не найден или не принадлежит владельцу.")
        await self._run(self._confirm_sync, server["aternos_id"])
        await database.log_action(
            self.owner_id, "server_confirm", server["display_name"], server_id=server_id
        )

    async def probe_cookie(self, cookie: str) -> None:
        """Проверяет куку через реальный вход в Aternos (без сохранения).

        Используется в онбординге и перед обновлением сессии владельца.
        Бросает AternosError при недействительной куке.
        """
        lock = self._locks.setdefault(self.owner_id, asyncio.Lock())
        async with lock:
            try:
                await asyncio.to_thread(self._get_client_sync, cookie)
            except AternosError:
                raise
            except Exception as exc:
                logger.exception("owner %s: проверка куки не удалась: %s", self.owner_id, exc)
                raise AternosError(f"Не удалось проверить куку Aternos: {exc}") from exc

    async def probe_session(self, cookie: str) -> List[dict]:
        """Вход + список серверов аккаунта (для онбординга), без сохранения.

        Возвращает [{aternos_id, server_ip, display_name}].
        """
        lock = self._locks.setdefault(self.owner_id, asyncio.Lock())
        async with lock:
            try:
                return await asyncio.to_thread(self._list_servers_sync, cookie)
            except AternosError:
                raise
            except Exception as exc:
                logger.exception("owner %s: список серверов не получен: %s", self.owner_id, exc)
                raise AternosError(f"Не удалось получить список серверов: {exc}") from exc

    async def update_session(self, new_cookie: str) -> None:
        """Обновляет куку владельца: шифрует, сохраняет в БД, сбрасывает кеш."""
        from services.crypto import encrypt_session

        cookie = (new_cookie or "").strip()
        if not cookie:
            raise AternosError("Кука не может быть пустой.")
        # Проверяем куку до сохранения.
        await self.probe_cookie(cookie)
        await database.update_owner_session(self.owner_id, encrypt_session(cookie))
        self._client_cache.pop(self.owner_id, None)
        await database.log_action(self.owner_id, "session_update", "кука обновлена")
        logger.info("owner %s: кука Aternos обновлена", self.owner_id)
