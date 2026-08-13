"""Публичный статус серверов Minecraft.

Три источника статуса (в порядке попыток):
  1. REST API mcsrvstat.us — быстрый путь для обычных серверов;
  2. modern Server List Ping (SLP) на «правильном» порту — порт из
     server_meta / переданного параметра / ответа mcsrvstat API / 25565;
  3. legacy ping (0xFE 0x01) — Aternos-серверы часто отвечают только на него
     (modern SLP молчит), поэтому это страховка для них.

Никогда не бросает исключений: недоступность источника = offline-словарь.
"""

from __future__ import annotations

import asyncio
import json
import logging
import struct
from typing import Dict, List, Optional

import aiohttp

logger = logging.getLogger(__name__)

API_URL = "https://api.mcsrvstat.us/3/{ip}"
LEGACY_PING_TIMEOUT = 5.0  # сек
MODERN_SLP_TIMEOUT = 4.0   # сек
REQUEST_TIMEOUT = 7.0      # сек, для mcsrvstat.us
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
        "port": int(data["port"]) if data.get("port") else None,
    }


async def _modern_slp(host: str, port: int) -> Optional[dict]:
    """Modern Server List Ping (1.7+). Возвращает payload или None.

    Handshake + Status Request; ответ — JSON: players.online/max,
    description, version.name. Некоторые серверы (в т.ч. Aternos) молчат.
    """
    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_connection(host, int(port)), timeout=MODERN_SLP_TIMEOUT
        )
        try:
            handshake = bytes([0x00, 0x04]) + b"\x09localhost" + struct.pack(">H", port) + bytes([0x01])
            writer.write(struct.pack(">I", len(handshake)) + handshake)
            writer.write(b"\x01\x00")
            await writer.drain()

            length = await asyncio.wait_for(reader.read(5), timeout=MODERN_SLP_TIMEOUT)
            if not length or length[0] != 0x01:
                logger.info("modern SLP %s:%s: неверный ответ %r", host, port, length[:4])
                return None
            varint_len = 0
            shift = 0
            i = 1
            while i < len(length) and i < 5:
                varint_len |= (length[i] & 0x7F) << shift
                if not (length[i] & 0x80):
                    break
                shift += 7
                i += 1
            payload = b""
            while len(payload) < varint_len:
                chunk = await asyncio.wait_for(reader.read(varint_len - len(payload)), timeout=MODERN_SLP_TIMEOUT)
                if not chunk:
                    break
                payload += chunk
        finally:
            writer.close()
            try:
                await writer.wait_closed()
            except Exception:
                pass

        data = json.loads(payload.decode("utf-8", errors="replace"))
        players = data.get("players") or {}
        description = data.get("description") or {}
        motd = description.get("text") if isinstance(description, dict) else str(description)
        if isinstance(motd, list):
            motd = " ".join(str(p.get("text", "")) for p in motd if isinstance(p, dict))
        version = (data.get("version") or {}).get("name") or "Неизвестно"
        return {
            "ip": f"{host}:{port}",
            "is_online": True,
            "players_online": int(players.get("online") or 0),
            "players_max": int(players.get("max") or 0),
            "player_names": [str(p.get("name", "")) for p in players.get("sample", []) if isinstance(p, dict) and p.get("name")],
            "version": str(version),
        }
    except (asyncio.TimeoutError, OSError, ConnectionError, ValueError, json.JSONDecodeError) as exc:
        logger.info("modern SLP %s:%s не ответил: %s", host, port, exc)
        return None
    except Exception as exc:
        logger.warning("modern SLP %s:%s: ошибка: %s", host, port, exc)
        return None


async def _legacy_ping(host: str, port: int) -> Optional[dict]:
    """Legacy Server List Ping (0xFE 0x01). Возвращает payload или None.

    Ответ (1.7+): 0xFF 0x00 [len] [поля через \\x00]:
      MOTD | PROTOCOL | VERSION | MOTD2 | PLAYERS_ONLINE | MAX_PLAYERS
    Иногда встречаются пустые поля (двойные разделители) — они фильтруются.
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

        text = remaining.decode("utf-16-be", errors="replace")
        fields = [f.strip() for f in text.split("\x00") if f.strip()]
        if len(fields) < 3:
            logger.warning("legacy ping %s:%s: битый ответ: %r", host, port, text[:120])
            return None

        def _to_int(value: str) -> int:
            try:
                return int(value)
            except (TypeError, ValueError):
                return 0

        # Расширенный формат: [motd, protocol, version, motd2, online, max].
        if len(fields) >= 6 and _to_int(fields[1]) > 0 and fields[2].isdigit() is False:
            players_online = _to_int(fields[4])
            players_max = _to_int(fields[5])
            version = fields[2]
        else:
            # Базовый формат: [motd, online, max].
            players_online = _to_int(fields[1])
            players_max = _to_int(fields[2])
            version = "Неизвестно"

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

    Порядок попыток: mcsrvstat API -> modern SLP на правильном порту ->
    legacy ping. «Правильный порт»: переданный (server_meta) -> порт из
    ответа API -> 25565. Никогда не бросает исключений.
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

    api_payload = await _api_status(server_ip)
    if api_payload is not None and api_payload.get("is_online"):
        return api_payload

    api_port = (api_payload or {}).get("port")
    target_port = explicit_port or api_port or 25565

    slp = await _modern_slp(host, target_port)
    if slp is not None:
        return slp

    legacy = await _legacy_ping(host, target_port)
    if legacy is not None:
        return legacy

    if api_payload is not None:
        return api_payload
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
