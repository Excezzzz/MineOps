"""Публичный статус серверов Minecraft.

Два источника статуса:
  1. Прямой legacy-ping (протокол 0xFE 0x01) на (host, порт). Aternos-серверы
     часто не отвечают на modern status protocol (mcsrvstat.us их не видит),
     но всегда отвечают на legacy-ping — это основной путь.
  2. REST API mcsrvstat.us (fallback, если legacy-ping не ответил и порт
     неизвестен, или для серверов вне Aternos).
"""

from __future__ import annotations

import asyncio
import logging
from typing import Dict, List, Optional

import aiohttp

logger = logging.getLogger(__name__)

API_URL = "https://api.mcsrvstat.us/3/{ip}"
LEGACY_PING_TIMEOUT = 6.0  # сек
REQUEST_TIMEOUT = 7.0  # сек, для mcsrvstat.us
HTTP_UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
    " (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)


def _empty_payload(server_ip: str) -> dict:
    """Стандартизированный ответ при недоступности сервера."""
    return {
        "ip": server_ip,
        "is_online": False,
        "players_online": 0,
        "players_max": 0,
        "player_names": [],
        "version": "Неизвестно",
    }


def _parse_payload(server_ip: str, data: dict) -> dict:
    """Нормализует JSON mcsrvstat в стандартизированный словарь."""
    players = data.get("players") or {}
    raw_names: List[dict] = players.get("list") or []
    return {
        "ip": server_ip,
        "is_online": bool(data.get("online", False)),
        "players_online": int(players.get("online") or 0),
        "players_max": int(players.get("max") or 0),
        "player_names": [
            str(item["name"]) for item in raw_names if isinstance(item, dict) and item.get("name")
        ],
        "version": str(data.get("version") or "") or "Неизвестно",
    }


async def _legacy_ping(host: str, port: int) -> Optional[dict]:
    """Legacy Server List Ping (0xFE 0x01). Возвращает payload или None.

    Ответ (1.7+): 0xFF 0x00 [len] [поля через \\x00]:
    motd | players_online | players_max | protocol | version.
    На серверах Aternos modern-status может молчать, а legacy — отвечать.
    """
    logger.info("legacy ping к: %s:%s", host, port)
    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_connection(host, int(port)), timeout=LEGACY_PING_TIMEOUT
        )
        try:
            writer.write(b"\xfe\x01")
            await writer.drain()
            data = await asyncio.wait_for(reader.readexactly(3), timeout=LEGACY_PING_TIMEOUT)
            if data[:2] != b"\xff\x00":
                logger.warning("legacy ping %s:%s: неверный ответ %r", host, port, data)
                return None
            remaining = await asyncio.wait_for(
                reader.read(data[2] * 2), timeout=LEGACY_PING_TIMEOUT
            )
        finally:
            writer.close()
            try:
                await writer.wait_closed()
            except Exception:
                pass

        text = (data + remaining).decode("utf-16-be", errors="replace")
        fields = text.split("\x00")
        if len(fields) < 3:
            logger.warning("legacy ping %s:%s: битый ответ: %r", host, port, text[:120])
            return None

        def _to_int(value: str) -> int:
            try:
                return int(value)
            except (TypeError, ValueError):
                return 0

        players_online = _to_int(fields[1])
        players_max = _to_int(fields[2])
        version = fields[4].strip() if len(fields) > 4 and fields[4].strip() else "Неизвестно"
        return {
            "ip": f"{host}:{port}",
            "is_online": True,
            "players_online": players_online,
            "players_max": players_max,
            "player_names": [],
            "version": version,
        }
    except (asyncio.TimeoutError, OSError, ConnectionError, asyncio.IncompleteReadError) as exc:
        logger.info("legacy ping %s:%s не ответил: %s", host, port, exc)
        return None
    except Exception as exc:
        logger.warning("legacy ping %s:%s: ошибка: %s", host, port, exc)
        return None


async def _api_status(server_ip: str) -> Optional[dict]:
    """Статус через mcsrvstat.us API; None при недоступности API."""
    url = API_URL.format(ip=server_ip)
    logger.info("mcsrvstat запрос к: %s", url)
    timeout = aiohttp.ClientTimeout(total=REQUEST_TIMEOUT)
    try:
        async with aiohttp.ClientSession(timeout=timeout) as session:
            async with session.get(url, headers={"User-Agent": HTTP_UA}) as response:
                if response.status != 200:
                    logger.warning("mcsrvstat вернул HTTP %s для %s", response.status, server_ip)
                    return None
                data = await response.json()
        logger.info("mcsrvstat: %s -> онлайн=%s", server_ip, data.get("online"))
        return _parse_payload(server_ip, data)
    except (asyncio.TimeoutError, aiohttp.ClientError, ValueError) as exc:
        logger.warning("mcsrvstat недоступен для %s: %s", server_ip, exc)
        return None


async def get_server_status(server_ip: str, port: Optional[int] = None) -> dict:
    """Возвращает статус сервера по адресу `server_ip` (+ опциональный порт).

    Приоритет: legacy-ping (если известен порт или адрес уже с портом),
    затем mcsrvstat.us API. Никогда не бросает исключений.
    """
    if not server_ip:
        logger.warning("не задан адрес сервера для статуса")
        return _empty_payload("")

    host = server_ip
    explicit_port = port
    if ":" in server_ip and not server_ip.startswith("["):
        host, _, maybe_port = server_ip.rpartition(":")
        if maybe_port.isdigit():
            explicit_port = int(maybe_port)

    if explicit_port:
        payload = await _legacy_ping(host, explicit_port)
        if payload:
            return payload
        return _empty_payload(server_ip)

    api_payload = await _api_status(server_ip)
    if api_payload is not None:
        return api_payload

    # API недоступен — пробуем legacy-ping на стандартном порту.
    payload = await _legacy_ping(host, 25565)
    if payload:
        return payload
    return _empty_payload(server_ip)


async def get_servers_status(servers: List[dict]) -> Dict[str, dict]:
    """Статусы нескольких серверов (параллельный опрос).

    servers — список {server_ip, port?}; результат {server_ip: status}.
    """
    if not servers:
        return {}

    async def one(srv: dict) -> dict:
        return await get_server_status(str(srv["server_ip"]), srv.get("port"))

    statuses = await asyncio.gather(*(one(s) for s in servers))
    return {s["server_ip"]: st for s, st in zip(servers, statuses)}
