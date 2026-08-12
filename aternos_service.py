"""MineOps - async wrappers around python-aternos (control) and mcstatus (status).

All blocking library calls run inside asyncio.to_thread. NOTE: python-aternos
(3.0.6, unmaintained) exposes no backup API - create_backup() reports that
clearly instead of failing silently.
"""

from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass
from typing import Any, Optional

from mcstatus import JavaServer as MCJavaServer

logger = logging.getLogger(__name__)


class AternosError(RuntimeError):
    pass


@dataclass
class ServerInfo:
    online: bool = False
    address: str = ""
    port: int = 0
    version: str = ""
    players: int = 0
    max_players: int = 0
    latency_ms: float = 0.0
    error: str = ""


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
        self._client: Any = None
        self._server: Any = None
        self._login_lock = asyncio.Lock()
        self._op_lock = asyncio.Lock()

    def _login_sync(self) -> Any:
        from python_aternos import Client

        client = Client()
        if self.session:
            client.login_with_session(self.session)
        else:
            client.login(self.username, self.password)
        return client

    async def _get_server(self) -> Any:
        async with self._login_lock:
            if self._server is None:
                client = await asyncio.to_thread(self._login_sync)
                servers = await asyncio.to_thread(client.account.list_servers)
                if not servers:
                    raise AternosError("no Aternos servers found on this account")
                for candidate in servers:
                    try:
                        await asyncio.to_thread(candidate.fetch)
                    except Exception:
                        continue
                    if getattr(candidate, "domain", "") == self.server_address:
                        self._server = candidate
                        break
                self._server = self._server or servers[0]
                self._client = client
            return self._server

    @staticmethod
    def _is_running(server: Any) -> bool:
        status_num = getattr(server, "status_num", None)
        try:
            return int(status_num) in (1, 2, 6)
        except (TypeError, ValueError):
            return False

    async def start_server(self) -> str:
        async def _do(server: Any) -> str:
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
                raise AternosError(f"start failed: {exc}") from exc

    async def stop_server(self) -> str:
        async with self._op_lock:
            server = await self._get_server()
            try:
                await asyncio.to_thread(server.stop)
            except Exception as exc:
                raise AternosError(f"stop failed: {exc}") from exc
            return "Server stop requested."

    async def create_backup(self) -> str:
        async def _do(server: Any) -> str:
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
            return await asyncio.to_thread(_do, server)

    async def shutdown(self) -> None:
        if self._client is not None:
            kill = getattr(self._client, "kill_keep_alives", None)
            if callable(kill):
                try:
                    await asyncio.to_thread(kill)
                except Exception:
                    logger.warning("could not stop Aternos keep-alive", exc_info=True)

    async def get_status(self, timeout: float = 8.0) -> ServerInfo:
        # Never raises: offline/unreachable is an expected outcome.
        info = ServerInfo(address=self.server_address, port=self.server_port)

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
            info.players = int(getattr(players, "online", 0) or 0)
            info.max_players = int(getattr(players, "max", 0) or 0)
            info.version = str(getattr(version, "name", "") or "")
            info.latency_ms = round(float(getattr(status, "latency", 0.0) or 0.0), 1)
        except Exception as exc:
            info.error = str(exc) or exc.__class__.__name__

        return info