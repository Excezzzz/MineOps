"""Живой дашборд статусов (multi-tenant, принцип единого закрепа).

Каждые 30 секунд (шедулер в main.py) для каждого владельца опрашиваются
статусы его серверов через mcsrvstat.us, и в каждом привязанном чате
обновляется ЕДИНЫЙ закреплённый дашборд (edit_message_text). Если сообщение
удалено или закрепа ещё нет — дашборд отправляется и закрепляется заново.

Также здесь живёт рассылка broadcast_message по списку чатов (например,
уведомление о запуске сервера) и временные позиции очереди запуска.
"""

from __future__ import annotations

import logging
import time
from html import escape as quote_html

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest

import database
from keyboards.group_kb import get_dashboard_kb
from services import mcsrvstat

logger = logging.getLogger(__name__)

# Момент (unix-time), до которого серверы считаются «запускающимися».
_starting_until = 0

# Позиции в очереди Aternos по server_id (заполняет queue_watcher).
_queue_positions: dict[int, int] = {}

# Кеш статусов панели Aternos: server_id -> (момент запроса, панельный статус).
# Панель — авторитетный источник online/offline; кеш спасает от Cloudflare-банов
# (молотить Aternos каждые 30 с нельзя) и от врущего legacy-пинга.
_panel_cache: dict[int, tuple[float, dict]] = {}
# Бэкофф при Cloudflare-бане: server_id -> время, до которого панель не дёргаем.
_panel_fail_until: dict[int, float] = {}
PANEL_TTL_SECONDS = 60          # свежесть статуса панели
PANEL_FAIL_COOLDOWN = 5 * 60    # после неудачи панель не трогаем 5 минут


async def _ensure_server_port(owner_id: int, server_id: int) -> int | None:
    """Актуальный порт сервера: кеш (server_meta), если он достоверен.

    Редирект Aternos на 25565 отвечает фейком (0/0 игроков, «A Minecraft
    server»), поэтому пустой или «25565» порт запрашивается у панели
    Aternos один раз и сохраняется. Ошибки панели не фатальны.
    """
    port = await database.get_server_port(server_id)
    if port and port != 25565:
        return port
    try:
        from services.aternos_api import AternosError, AternosManager

        info = await AternosManager(owner_id).get_queue_status(server_id)
        panel_port = int(info.get("port") or 0) if info.get("port") else 0
        if panel_port and panel_port != 25565:
            await database.set_server_port(server_id, panel_port)
            logger.info(
                "сервер (id=%s): порт из панели Aternos = %s", server_id, panel_port
            )
            return panel_port
    except AternosError:
        pass
    return None


def mark_as_starting(seconds: int = 300) -> None:
    """Помечает серверы как запускающиеся на ближайшие `seconds` секунд."""
    global _starting_until
    _starting_until = time.time() + seconds


def clear_starting() -> None:
    """Сбрасывает пометку «запускается»."""
    global _starting_until
    _starting_until = 0


def is_starting() -> bool:
    """True, если серверы ещё в состоянии «запускается»."""
    return time.time() < _starting_until


def set_queue_position(server_id: int, position: int) -> None:
    """Запоминает позицию сервера в очереди запуска (для дашборда)."""
    _queue_positions[server_id] = position


def clear_queue_position(server_id: int) -> None:
    """Убирает позицию очереди (сервер стартовал или сдался)."""
    _queue_positions.pop(server_id, None)


def format_dashboard_text(servers: list[dict]) -> str:
    """Формирует ЕДИНЫЙ дашборд: подзаголовок и сводка для каждого сервера.

    servers — список словарей: {server_id, display_name, server_ip,
    is_online, players_online, players_max, player_names, version}.
    """
    if not servers:
        return "🔴 <b>В чате нет подключённых серверов.</b>\nВладелец: /link"

    blocks: list[str] = []
    for s in servers:
        ip = str(s.get("server_ip") or "Не указан")
        name = str(s.get("display_name") or ip)
        server_id = int(s.get("id") or 0)
        is_online = bool(s.get("is_online"))

        if is_online:
            clear_starting()
            clear_queue_position(server_id)
            status_line = f"🟢 <b>{quote_html(name)} — ОНЛАЙН</b>"
        else:
            position = _queue_positions.get(server_id, 0)
            if position > 0:
                status_line = (
                    f"🟡 <b>{quote_html(name)} — В ОЧЕРЕДИ (позиция {position})</b> ⏳\n"
                    "<i>(Ожидание запуска на Aternos...)</i>"
                )
            elif is_starting():
                status_line = (
                    f"🟡 <b>{quote_html(name)} — ЗАПУСКАЕТСЯ / В ОЧЕРЕДИ...</b> ⏳\n"
                    "<i>(Открытие портов Aternos занимает ~2-5 мин)</i>"
                )
            else:
                status_line = f"🔴 <b>{quote_html(name)} — ОФФЛАЙН</b>"

        players_online = s.get("players_online", 0)
        players_max = s.get("players_max", 0)
        player_list = s.get("player_list") or []
        lines = [status_line, f"🌐 IP: {quote_html(ip)}"]
        if is_online:
            # Игроки показываются только у живого сервера: у оффлайн-сервера
            # цифры «0/0» только путают (сервер даже не поднят).
            if player_list:
                players_line = "👥 Игроки ({}/{}):\n{}".format(
                    players_online,
                    players_max,
                    "\n".join(f"• {quote_html(str(n))}" for n in player_list[:20]),
                )
            else:
                players_line = f"👥 Игроки: {players_online}/{players_max}"
            lines.append(players_line)
        lines.append(f"📦 Версия: {quote_html(str(s.get('version', 'Неизвестно')) or 'Неизвестно')}")

        blocks.append("\n".join(lines))
    return "\n\n".join(blocks)


# Чаты, которым уже отправлена подсказка о правах администратора (для пина).
_pin_notice_sent: set[int] = set()
# Чаты, где пин дашборда ещё не удался: пока чат в этом сете, каждый тик
# пробуем закрепить снова. Без этого дашборд, созданный ДО выдачи боту
# прав админа, остался бы незакреплённым навсегда (тики правят сообщение,
# но не перепинивают его).
_pin_pending: set[int] = set()


async def _try_pin(bot: Bot, chat_id: int, message_id: int) -> bool:
    """Пробует закрепить сообщение; True — закреплено."""
    try:
        await bot.pin_chat_message(
            chat_id=chat_id, message_id=message_id, disable_notification=True
        )
        return True
    except Exception as exc:
        logger.warning("chat %s: не удалось закрепить дашборд (%s)", chat_id, exc)
        return False


async def _pin_if_needed(bot: Bot, chat_id: int, message_id: int) -> bool:
    """Закрепляет дашборд, если он ещё не закреплён (самовосстановление).

    Проверяет фактическое состояние через get_chat, а не по памяти:
    после рестарта бота `_pin_pending` пуст, а дашборд мог остаться
    незакреплённым — get_chat это находит и закрепляет.
    """
    try:
        chat_info = await bot.get_chat(chat_id)
        pinned_id = getattr(chat_info.pinned_message, "message_id", None)
        if pinned_id == message_id:
            return True
    except Exception as exc:
        logger.warning("chat %s: не удалось проверить закреп: %s", chat_id, exc)
        return False
    return await _try_pin(bot, chat_id, message_id)


async def _render_dashboard(bot: Bot, chat_id: int, text: str, kb) -> None:
    """Обновляет или (пере)создаёт закреплённый дашборд в одном чате.

    Любые Telegram-ошибки (сообщение удалено, нет прав на закрепление)
    перехватываются, чтобы сбой в одном чате не ронял остальные.
    id нового сообщения сохраняется ВСЕГДА — даже без закрепления, иначе
    следующий тик снова попытается править удалённое и заспамит чат.
    """
    pinned_msg_id = await database.get_chat_pinned_msg(chat_id)

    if pinned_msg_id is not None:
        try:
            await bot.edit_message_text(
                text=text, chat_id=chat_id, message_id=pinned_msg_id, reply_markup=kb
            )
            if chat_id in _pin_pending or not await _pin_if_needed(bot, chat_id, pinned_msg_id):
                _pin_pending.add(chat_id)
            else:
                _pin_pending.discard(chat_id)
            return
        except TelegramAPIError as exc:
            if "message is not modified" in str(exc):
                if chat_id in _pin_pending or not await _pin_if_needed(bot, chat_id, pinned_msg_id):
                    _pin_pending.add(chat_id)
                else:
                    _pin_pending.discard(chat_id)
                return  # контент не изменился — дашборд актуален
            logger.warning("chat %s: edit dashboard failed (%s), will re-send", chat_id, exc)
        # Падаем вниз и пересоздаём сообщение.

    try:
        msg = await bot.send_message(chat_id=chat_id, text=text, reply_markup=kb)
    except (TelegramAPIError, AttributeError) as exc:
        logger.warning("chat %s: cannot send dashboard (%s)", chat_id, exc)
        return

    pinned_ok = await _try_pin(bot, chat_id, msg.message_id)

    # Сохраняем id всегда: при неудачном пине следующий тик будет править
    # новое сообщение, а не плодить новые.
    await database.set_chat_pinned_msg(chat_id, msg.message_id)

    if pinned_ok:
        _pin_pending.discard(chat_id)
        _pin_notice_sent.discard(chat_id)
        logger.info("чат %s: дашборд создан и закреплён (msg %s)", chat_id, msg.message_id)
    else:
        _pin_pending.add(chat_id)
        if chat_id not in _pin_notice_sent:
            _pin_notice_sent.add(chat_id)
            try:
                await bot.send_message(
                    chat_id,
                    "⚠️ <b>Дайте боту права администратора в этой группе</b>, "
                    "чтобы он мог закреплять дашборд.\n"
                    "Настройки группы → Управление группами → администраторы → "
                    "добавить бота.",
                )
            except Exception as exc:
                logger.warning(
                    "chat %s: не удалось отправить подсказку о закрепе: %s",
                    chat_id, exc,
                )


async def _get_panel_cached(owner_id: int, server_id: int) -> dict | None:
    """Статус панели Aternos с кешем (PANEL_TTL_SECONDS) и бэкоффом.

    Возвращает панельный словарь; None — панель недоступна и кеша нет.
    При недоступности панели используется устаревший кеш (лучше, чем ничего);
    после неудачного запроса панель не дёргается PANEL_FAIL_COOLDOWN — так
    Cloudflare-бан не усугубляется нашей же молотилкой.
    """
    cached = _panel_cache.get(server_id)
    now = time.monotonic()
    if cached and now - cached[0] < PANEL_TTL_SECONDS:
        return cached[1]
    if now < _panel_fail_until.get(server_id, 0):
        return cached[1] if cached else None  # бэкофф: ждём, не дёргаем панель
    from services.aternos_api import AternosError, AternosManager

    try:
        panel = await AternosManager(owner_id).get_panel_status(server_id)
    except AternosError as exc:
        logger.warning(
            "server %s: статус панели не получен (%s) — бэкофф %s c",
            server_id, exc, PANEL_FAIL_COOLDOWN,
        )
        _panel_fail_until[server_id] = now + PANEL_FAIL_COOLDOWN
        if cached:
            return cached[1]  # устаревший кеш лучше, чем врущий legacy-пинг
        return None
    _panel_cache[server_id] = (now, panel)
    _panel_fail_until.pop(server_id, None)
    return panel


def _panel_to_status(server: dict, panel: dict) -> dict:
    """Строит стандартный статус-словарь из данных панели Aternos."""
    online = int(panel.get("panel_status") or 0) == 1
    names = [str(n) for n in (panel.get("playerlist") or [])]
    port = int(panel.get("port") or 0) or None
    return {
        "ip": str(server.get("server_ip") or ""),
        "is_online": online,
        "players_online": int(panel.get("players") or 0) if online else 0,
        "players_max": int(panel.get("slots") or 0) if online else 0,
        "player_names": names if online else [],
        "player_list": names if online else [],
        "version": str(panel.get("version") or "") or "Неизвестно",
        "port": port,
    }


async def get_authoritative_status(owner_id: int, server: dict) -> dict:
    """Авторитетный статус сервера: панель Aternos -> честный mcsrvstat.

    Панель знает точный online/offline, игроков и ники; публичный
    legacy-пинг на Aternos врёт (прокси отвечает на оффлайн-серверах),
    поэтому при недоступности панели он отключается (allow_legacy=False).
    """
    panel = await _get_panel_cached(owner_id, int(server["id"]))
    if panel is not None:
        return _panel_to_status(server, panel)
    port = await _ensure_server_port(int(server.get("owner_id") or owner_id), int(server["id"]))
    return await mcsrvstat.get_server_status(
        server["server_ip"], port=port, allow_legacy=False
    )


async def _update_chat_dashboard(bot: Bot, chat_id: int) -> None:
    """Обновляет дашборд одного чата (по привязанным серверам)."""
    servers = await database.get_chat_servers(chat_id)
    if not servers:
        return  # владелец ещё не привязал серверы — дашборд не нужен

    for s in servers:
        port = await _ensure_server_port(int(s["owner_id"]), s["id"])
        s["port"] = port
    statuses = {
        s["server_ip"]: await get_authoritative_status(int(s["owner_id"]), s)
        for s in servers
    }
    for s in servers:
        status = statuses.get(s["server_ip"], {})
        known_port = await database.get_server_port(s["id"])
        status_port = status.get("port")
        # Пишем порт только при живом ответе: оффлайн-фолбэк отдаёт
        # дефолтный 25565 и мог бы перетереть реальный порт из панели.
        if status.get("is_online") and status_port and status_port != known_port:
            await database.set_server_port(s["id"], status_port)
            logger.info(
                "сервер %s (id=%s): порт обновлён на %s", s["server_ip"], s["id"], status_port
            )
    merged = [{**s, **statuses.get(s["server_ip"], {})} for s in servers]
    await _render_dashboard(bot, chat_id, format_dashboard_text(merged), get_dashboard_kb(merged, chat_id))


async def _update_owner_pm_dashboard(bot: Bot, owner: dict, merged: list[dict]) -> None:
    """Закреплённый дашборд владельца в ЛС: обновляет или (пере)создаёт.

    merged — список серверов со статусами (как у format_dashboard_text);
    пустой список — рендерится заглушка, чтобы пин не был устаревшим.
    """
    text = (
        format_dashboard_text(merged)
        if merged
        else "🔴 <b>Нет подключённых серверов.</b>\n"
        "Откройте /panel и нажмите «🔄 Обновить серверы»."
    )
    pm_pinned = await database.get_owner_pm_pinned(int(owner["user_id"]))
    if pm_pinned:
        try:
            await bot.edit_message_text(
                text=text,
                chat_id=int(owner["user_id"]),
                message_id=pm_pinned,
            )
            return
        except TelegramBadRequest as exc:
            if "message is not modified" in str(exc):
                return  # контент не изменился — дашборд актуален
            logger.warning(
                "owner %s: edit PM dashboard failed (%s), will re-send",
                owner["user_id"], exc,
            )
        except TelegramAPIError as exc:
            logger.warning(
                "owner %s: edit PM dashboard failed (%s), will re-send",
                owner["user_id"], exc,
            )
    try:
        msg = await bot.send_message(int(owner["user_id"]), text=text)
    except Exception as exc:
        logger.warning("owner %s: PM-дашборд не отправлен: %s", owner["user_id"], exc)
        return
    pinned_ok = False
    try:
        await bot.pin_chat_message(
            chat_id=int(owner["user_id"]),
            message_id=msg.message_id,
            disable_notification=True,
        )
        pinned_ok = True
    except Exception as exc:
        logger.warning("owner %s: PM-дашборд не закреплён: %s", owner["user_id"], exc)
    # id сохраняется всегда — при неудачном пине следующий тик правит
    # новое сообщение, а не плодит новые.
    await database.set_owner_pm_pinned(int(owner["user_id"]), msg.message_id)
    if pinned_ok:
        logger.info(
            "owner %s: PM-дашборд создан и закреплён (msg %s)",
            owner["user_id"], msg.message_id,
        )


async def update_dashboards(bot: Bot) -> None:
    """Обновляет дашборды всех владельцев в их привязанных чатах.

    У каждого владельца независимый опрос его серверов (per-owner polling).
    Статус опрашивается для каждого сервера владельца даже без привязанных
    чатов — диагностические логи показывают реальную картину.
    """
    logger.info("=== DASHBOARD TICK ===")
    owners = await database.get_all_owners()
    logger.info("owners: %d", len(owners))
    for owner in owners:
        servers = await database.get_servers_by_owner(owner["user_id"])
        logger.info("owner %s: %d серверов", owner["user_id"], len(servers))
        statuses_by_server: dict[int, dict] = {}
        for server in servers:
            chats = await database.get_chats_for_server(server["id"])
            logger.info(
                "server %s (id=%s): %d чатов",
                server["server_ip"], server["id"], len(chats),
            )
            port = await _ensure_server_port(int(owner["user_id"]), server["id"])
            status = await get_authoritative_status(int(owner["user_id"]), server)
            statuses_by_server[int(server["id"])] = status
            logger.info(
                "server %s: online=%s (players %s/%s, port=%s)",
                server["server_ip"],
                status.get("is_online"),
                status.get("players_online"),
                status.get("players_max"),
                port,
            )
            if status.get("is_online"):
                clear_starting()
                clear_queue_position(server["id"])
            status_port = status.get("port")
            # Пишем порт только при живом ответе (оффлайн-фолбэк = 25565).
            if status.get("is_online") and status_port and status_port != port:
                await database.set_server_port(server["id"], status_port)
                logger.info(
                    "server %s (id=%s): порт обновлён на %s",
                    server["server_ip"], server["id"], status_port,
                )
        for chat in await database.get_chats_by_owner(owner["user_id"]):
            await _update_chat_dashboard(bot, int(chat["chat_id"]))
        # Закреплённый дашборд в ЛС: статусы ВСЕХ серверов владельца.
        pm_merged = [
            {**s, **statuses_by_server.get(int(s["id"]), {})} for s in servers
        ]
        await _update_owner_pm_dashboard(bot, owner, pm_merged)


async def update_chats_dashboards(bot: Bot, chat_ids: list[int]) -> None:
    """Обновляет дашборды в конкретных чатах (после запуска/остановки)."""
    for chat_id in chat_ids:
        await _update_chat_dashboard(bot, chat_id)


# Владельцы, которым уже отправлено уведомление о просроченной куке.
_session_broken_notified: set[int] = set()


async def check_all_sessions(bot: Bot) -> None:
    """Проверяет куки всех владельцев; при просрочке шлёт уведомление в ЛС.

    Уведомление отправляется один раз за период просрочки (до восстановления).
    """
    from services.aternos_api import AternosError, AternosManager

    owners = await database.get_all_owners()
    for owner in owners:
        uid = int(owner["user_id"])
        try:
            await AternosManager(uid).check_session()
        except AternosError as exc:
            if "Cloudflare" in str(exc):
                continue  # временный бан запросов — не про куку
            if uid in _session_broken_notified:
                continue
            _session_broken_notified.add(uid)
            await database.log_action(uid, "session_expired", "кука Aternos просрочена")
            try:
                await bot.send_message(
                    uid,
                    "⚠️ <b>Кука Aternos просрочена или недействительна!</b>\n\n"
                    "Обновите её: /set_session или кнопка «🔄 Обновить куку» в панели /panel.",
                )
            except Exception as exc2:
                logger.warning(
                    "owner %s: не удалось отправить уведомление о куке: %s", uid, exc2
                )
            continue
        _session_broken_notified.discard(uid)


async def broadcast_message(bot: Bot, chat_ids: list[int], text: str) -> None:
    """Рассылает `text` в указанные чаты (ошибки отдельных чатов не фатальны)."""
    for chat_id in chat_ids:
        try:
            await bot.send_message(chat_id=chat_id, text=text)
        except Exception as exc:
            logger.warning("broadcast: не удалось отправить в chat %s: %s", chat_id, exc)
