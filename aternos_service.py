"""MineOps - async wrappers around python-aternos (control) and mcstatus (status).

All blocking library calls run inside asyncio.to_thread. The authenticated
python-aternos Client is cached on self.client and reused for the bot's whole
lifetime: re-authentication only happens when the cached session expires or
the client was never created (prevents Cloudflare anti-bot blocks / rate
limits caused by logging in on every request). NOTE: python-aternos
(3.0.6, unmaintained) exposes no backup API - create_backup() reports that
clearly instead of failing silently.
"""

from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass, field
from typing import Any, List, Optional

from mcstatus import JavaServer as MCJavaServer

logger = logging.getLogger(__name__)


class AternosError(RuntimeError):
    pass


AUTH_ERROR_MSG = ("❌ Ошибка авторизации Aternos: проверьте ATERNOS_USERNAME и "
                  "ATERNOS_PASSWORD в файле .env")


@dataclass
class ServerInfo:
    online: bool = False
    state: str = "offline"
    address: str = ""
    port: int = 0
    version: str = ""
    players: int = 0
    max_players: int = 0
    players_list: List[str] = field(default_factory=list)
    latency_ms: float = 0.0
    error: str = ""


# python-aternos server.status values that mean "booting up" vs "stopped".
STATUS_STARTING = {"starting", "start", "loading", "preparing", "restarting", "restart"}


class AternosService:
    def __init__(
        self,
        username: str,
        password: str,
        server_address: str,
        server_port: int,
        session: Optional[str] = None,
    ) -> None:
        self.username = username
        self.password = password
        self.server_address = server_address
        self.server_port = int(server_port)
        self.session = session
        self.client: Any = None
        self._server: Any = None
        self._login_lock = asyncio.Lock()
        self._op_lock = asyncio.Lock()

    def _login_sync(self) -> Any:
        """Create a cached python-aternos Client (session-first, then credentials)."""
        from python_aternos import Client
        from python_aternos.aterrors import AternosError as AtAternosError

        client = Client()
        if self.session:
            # Reuse the saved session when the client is missing/expired.
            try:
                client.login_with_session(self.session)
                return client
            except (AtAternosError, Exception) as exc:
                logger.warning("Aternos session rejected (%s), falling back to credentials", exc)
        client.login(self.username, self.password)
        return client

    async def _get_server(self) -> Any:
        async with self._login_lock:
            if self._server is not None and self.client is not None:
                # Reuse the authenticated session - do NOT re-login on every tick.
                return self._server
            client = None
            try:
                client = await asyncio.to_thread(self._login_sync)
            except Exception as exc:
                from python_aternos.aterrors import CredentialsError

                if isinstance(exc, CredentialsError):
                    raise AternosError(AUTH_ERROR_MSG) from exc
                raise AternosError(f"Aternos login failed: {exc}") from exc
            try:
                servers = await asyncio.to_thread(client.account.list_servers)
            except Exception as exc:
                raise AternosError(f"could not load Aternos servers: {exc}") from exc
            if not servers:
                raise AternosError("no Aternos servers found on this account")
            server = None
            for candidate in servers:
                try:
                    await asyncio.to_thread(candidate.fetch)
                except Exception:
                    continue
                if getattr(candidate, "domain", "") == self.server_address:
                    server = candidate
                    break
            self.client = client
            self._server = server or servers[0]
            return self._server

    @staticmethod
    def _is_running(server: Any) -> bool:
        status_num = getattr(server, "status_num", None)
        try:
            return int(status_num) in (1, 2, 6)
        except (TypeError, ValueError):
            return False

    async def start_server(self) -> str:
        def _do(server: Any) -> str:
            if self._is_running(server):
                return "Server is already online or still starting up."
            try:
                server.start()
            except Exception:
                logger.exception("regular start() failed, retrying with headstart")
                server.start(headstart=True)
            return "Server start requested - it takes a few minutes to go online."

        async with self._op_lock:
            server = await self._get_server()
            try:
                return await asyncio.to_thread(_do, server)
            except Exception as exc:
                from python_aternos.aterrors import CredentialsError

                if isinstance(exc, CredentialsError):
                    raise AternosError(AUTH_ERROR_MSG) from exc
                raise AternosError(f"start failed: {exc}") from exc

    async def stop_server(self) -> str:
        async with self._op_lock:
            server = await self._get_server()
            try:
                await asyncio.to_thread(server.stop)
            except Exception as exc:
                from python_aternos.aterrors import CredentialsError

                if isinstance(exc, CredentialsError):
                    raise AternosError(AUTH_ERROR_MSG) from exc
                raise AternosError(f"stop failed: {exc}") from exc
            return "Server stop requested."

    async def create_backup(self) -> str:
        def _do(server: Any) -> str:
            create = getattr(getattr(server, "backups", None), "create", None)
            if callable(create):
                create()
                return "Backup creation requested."
            raise AternosError(
                "Aternos backups are not supported by python-aternos (unmaintained, "
                "no backup API). Create backups from the Aternos panel instead."
            )

        async with self._op_lock:
            server = await self._get_server()
            try:
                return await asyncio.to_thread(_do, server)
            except Exception as exc:
                from python_aternos.aterrors import CredentialsError

                if isinstance(exc, CredentialsError):
                    raise AternosError(AUTH_ERROR_MSG) from exc
                raise AternosError(f"backup failed: {exc}") from exc

    async def shutdown(self) -> None:
        if self.client is not None:
            kill = getattr(self.client, "kill_keep_alives", None)
            if callable(kill):
                try:
                    await asyncio.to_thread(kill)
                except Exception:
                    logger.warning("could not stop Aternos keep-alive", exc_info=True)

    async def get_status(self, timeout: float = 8.0) -> ServerInfo:
        # Never raises: offline/unreachable is an expected outcome.
        #
        # Hybrid loop: routine updates hit the PUBLIC mcsrvstat API first
        # (no Aternos panel request => no Cloudflare friction). The cached
        # python-aternos session is consulted only to distinguish
        # "offline" from "starting/loading" when mcsrvstat says offline.
        info = ServerInfo(address=self.server_address, port=self.server_port)

        data = await self._fetch_status_api(timeout)
        if data is not None:
            if data.get("online"):
                self._apply_status_payload(info, data)
                return info
            # mcsrvstat says offline: check the Aternos panel (cached session)
            panel = await self._panel_state()
            if panel == "starting":
                info.state = "starting"
                info.error = "The server is starting up; it can take a few minutes."
            elif panel == "online":
                # Panel disagrees with the API (API cache lag): trust the panel.
                server = await self._cached_server_safe()
                if server is not None:
                    self._apply_server_props(info, server)
                else:
                    info.error = "Server is offline."
            else:
                info.state = "offline"
                info.error = "Server is offline." if panel == "offline" else "Server status unknown."
            return info

        # mcsrvstat unreachable: fall back to the Aternos panel as source of truth.
        server = await self._cached_server_safe()
        if server is not None:
            try:
                await asyncio.to_thread(server.fetch)
            except Exception:
                pass
            self._apply_server_props(info, server)
            return info
        # Panel also unreachable: last-resort direct socket probe.
        await self._probe_direct(info, timeout)
        if not info.online and not info.error:
            info.error = "Server is offline."
        return info

    async def _cached_server_safe(self) -> Optional[Any]:
        try:
            return await self._get_server()
        except AternosError as exc:
            logger.warning("aternos panel unavailable: %s", exc)
            return None

    async def _panel_state(self) -> Optional[str]:
        """Best-effort Aternos state: 'online' / 'starting' / 'offline' / None."""
        try:
            server = await self._get_server()
            await asyncio.to_thread(server.fetch)
            raw = str(getattr(server, "status", "") or "").strip().lower()
            if raw in STATUS_STARTING:
                return "starting"
            if raw == "online":
                return "online"
            return "offline"
        except Exception as exc:
            logger.debug("panel state check failed: %s", exc)
            return None

    def _apply_server_props(self, info: ServerInfo, server: Any) -> None:
        """Fill ServerInfo straight from the python-aternos web-panel data."""
        raw = str(getattr(server, "status", "") or "").strip().lower()
        if raw in STATUS_STARTING:
            info.state = "starting"
        elif raw == "online":
            info.state = "online"
        else:
            info.state = "offline"
        if info.state == "online":
            info.online = True
        elif info.state == "starting":
            info.error = "The server is starting up; it can take a few minutes."

        info.players = int(getattr(server, "players_count", 0) or 0)
        info.max_players = int(
            getattr(server, "slots", 0)
            or getattr(server, "max_players", 0)
            or 0
        )
        info.players_list = self._normalize_players(getattr(server, "players_list", None) or [])
        software = str(getattr(server, "software", "") or "").strip()
        version = str(getattr(server, "version", "") or "").strip()
        composed = " ".join(p for p in (software, version) if p).strip()
        if composed:
            info.version = composed
        info.address = str(getattr(server, "domain", "") or info.address)
        info.port = int(getattr(server, "port", 0) or 0) or info.port

    @staticmethod
    def _normalize_players(raw: Any) -> List[str]:
        names: List[str] = []
        for entry in raw:
            if isinstance(entry, dict):
                name = entry.get("name")
                if name:
                    names.append(str(name))
            elif isinstance(entry, str) and entry.strip():
                names.append(entry.strip())
        return names[:10]

    async def _fetch_status_api(self, timeout: float = 5.0) -> Optional[dict]:
        """mcsrvstat public API: resolves Aternos dynip/dynamic ports for us."""
        try:
            import aiohttp

            url = f"https://api.mcsrvstat.us/3/{self.server_address}"
            params = {"port": str(self.server_port)} if self.server_port != 25565 else None
            async with aiohttp.ClientSession() as session:
                async with session.get(
                    url,
                    params=params,
                    timeout=aiohttp.ClientTimeout(total=timeout),
                ) as resp:
                    if resp.status == 200:
                        return await resp.json()
                    logger.debug("mcsrvstat HTTP %s", resp.status)
        except Exception as exc:
            logger.debug("mcsrvstat lookup failed: %s", exc)
        return None

    @staticmethod
    def _apply_status_payload(info: ServerInfo, data: dict) -> None:
        info.online = True
        info.state = "online"
        info.address = str(data.get("ip") or info.address)
        info.port = int(data.get("port") or 0) or info.port
        version = data.get("version") or {}
        players = data.get("players") or {}
        info.version = str(version.get("name") or "")
        info.players = int(players.get("online") or 0)
        info.max_players = int(players.get("max") or 0)
        info.players_list = [
            str(p["name"]) for p in (players.get("list") or []) if isinstance(p, dict) and p.get("name")
        ][:10]
        info.latency_ms = round(float(data.get("ping") or 0.0), 1)
        info.error = ""

    async def _probe_direct(self, info: ServerInfo, timeout: float = 8.0) -> None:
        """Direct mcstatus ping (works when the port is static and reachable)."""

        def _lookup() -> Any:
            if hasattr(MCJavaServer, "lookup"):
                return MCJavaServer.lookup(f"{self.server_address}:{self.server_port}", timeout=timeout)
            return MCJavaServer(self.server_address, self.server_port)

        try:
            server = await asyncio.to_thread(_lookup)
            if asyncio.iscoroutine(server):
                server = await server
            if hasattr(server, "async_status"):
                status = await server.async_status()
            else:
                status = await asyncio.to_thread(server.status)
            players = getattr(status, "players", None)
            version = getattr(status, "version", None)
            info.online = True
            info.state = "online"
            info.players = int(getattr(players, "online", 0) or 0)
            info.max_players = int(getattr(players, "max", 0) or 0)
            info.players_list = [
                str(getattr(p, "name", p)) for p in (getattr(players, "names", None) or [])
            ][:10]
            info.version = str(getattr(version, "name", "") or "")
            info.latency_ms = round(float(getattr(status, "latency", 0.0) or 0.0), 1)
            info.error = ""
        except Exception as exc:
            info.error = str(exc) or exc.__class__.__name__