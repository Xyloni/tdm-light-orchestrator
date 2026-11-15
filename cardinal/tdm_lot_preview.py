"""Read-only preview of a Cardinal product file against TDM inventories."""
import argparse
import datetime as dt
import json
import logging
import hashlib
import html
import http.client
import socket
import os
from pathlib import Path
from contextlib import nullcontext
import re
from threading import Lock, Thread
from urllib import error, request

NAME = "TDM Drops lot preview"
VERSION = "0.2.0"
DESCRIPTION = "Checks the inventory of unsold accounts in a configured lot."
CREDITS = "TDM Light Orchestrator"
UUID = "b66cfbb9-9fe6-40e1-b9da-cf1d0e326542"
SETTINGS_PAGE = False
BIND_TO_DELETE = None

LOG = logging.getLogger(__name__)
URL = "http://127.0.0.1:8787/internal/lot-preview"
LOCAL_HTTP = request.build_opener(request.ProxyHandler({}))
LOGIN = re.compile(r"[a-zA-Z0-9_]+")


class UnixHTTPConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        try:
            self.sock.connect(os.environ["ORCHESTRATOR_CARDINAL_SOCKET"])
        except OSError:
            self.sock.close()
            raise


class UnixHTTPHandler(request.HTTPHandler):
    def http_open(self, req):
        return self.do_open(UnixHTTPConnection, req)


def open_orchestrator(req):
    if os.environ.get("ORCHESTRATOR_CARDINAL_SOCKET"):
        opener = request.build_opener(request.ProxyHandler({}), UnixHTTPHandler())
        return opener.open(req, timeout=13)
    return LOCAL_HTTP.open(req, timeout=13)


def read_logins(path, product_format):
    logins = []
    seen = set()
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        line = line.strip()
        if not line:
            continue
        fields = line.split(":", 1) if product_format == "login-password" else line.split(":")
        expected = 2 if product_format == "login-password" else 6
        index = 0 if product_format == "login-password" else 1
        if len(fields) != expected or not LOGIN.fullmatch(fields[index].strip()):
            raise ValueError(f"Unsupported product format at line {number}")
        login = fields[index].strip().lower()
        if login not in seen:
            logins.append((number, login))
            seen.add(login)
    return logins


def inspect_inventory(game, campaigns, login, token):
    payload = json.dumps({"Game": game, "Campaigns": campaigns, "Login": login}).encode()
    http_request = request.Request(URL, data=payload, headers={
        "Authorization": "Bearer " + token,
        "Content-Type": "application/json",
    })
    try:
        with open_orchestrator(http_request) as response:
            result = json.load(response)
    except error.HTTPError as exc:
        return {"state": "unknown", "http_status": exc.code}
    except (OSError, ValueError):
        return {"state": "unknown"}
    if result.get("Missing"):
        state = "missing_from_inventory"
    elif result.get("Unclaimed"):
        state = "awaiting_claim"
    elif result.get("Claimed", 0):
        state = "claimed_delivery_unverified"
    else:
        state = "unknown"
    return {"state": state, "inventory": result}


def suggested_cutoff(settings, campaigns, now):
    deadlines = settings.get("verified_deadlines", [])
    if {rule["campaign_id"] for rule in deadlines} != set(campaigns):
        return None
    expiry_times = []
    sources = []
    for rule in deadlines:
        expires = dt.datetime.fromisoformat(rule["expires_at"])
        if expires.tzinfo is None or not rule["source"].startswith("https://"):
            raise ValueError("Verified deadlines need a timezone and an HTTPS source")
        expiry_times.append(expires)
        sources.append(rule["source"])
    reserve = int(settings.get("buffer_hours", 24))
    if reserve < 0:
        raise ValueError("buffer_hours cannot be negative")
    cutoff = min(expiry_times) - dt.timedelta(hours=reserve)
    return {"at": cutoff.isoformat(), "past": cutoff <= now, "sources": sources}


def preview(config_path):
    return preview_settings(json.loads(config_path.read_text(encoding="utf-8-sig")))


def preview_settings(settings, product_lock=None):
    token = os.environ.get("ORCHESTRATOR_LOT_PREVIEW_TOKEN", "")
    if len(token) < 32:
        raise ValueError("ORCHESTRATOR_LOT_PREVIEW_TOKEN must be set")
    product_format = settings["format"]
    if product_format not in ("full", "login-password"):
        raise ValueError("Choose full or login-password product format")
    path = Path(settings["products_path"])
    with product_lock or nullcontext():
        modified = path.stat().st_mtime_ns
        logins = read_logins(path, product_format)
    limit = int(settings.get("sample_limit", 10))
    if not 1 <= limit <= 100:
        raise ValueError("sample_limit must be from 1 to 100")
    if settings.get("use_telegram_selection", False):
        selection_request = request.Request(URL, headers={"Authorization": "Bearer " + token})
        with open_orchestrator(selection_request) as response:
            selection = json.load(response)
        game, campaigns = selection["Game"], selection["Campaigns"]
    else:
        game, campaigns = settings["game"], settings["campaign_ids"]
    if not isinstance(campaigns, list) or not 1 <= len(campaigns) <= 20 or not all(isinstance(i, str) and i for i in campaigns):
        raise ValueError("Specify 1 to 20 campaign IDs")
    now = dt.datetime.now(dt.timezone.utc)
    cutoff = suggested_cutoff(settings, campaigns, now)
    # Include only row numbers in the report. Send the lookup login over loopback.
    rows = [dict(row=index, **inspect_inventory(game, campaigns, login, token))
            for index, login in logins[:limit]]
    return {
        "lot_id": str(settings["lot_id"]),
        "game": game,
        "campaign_count": len(campaigns),
        "started_at": now.isoformat(),
        "products_changed": modified != path.stat().st_mtime_ns,
        "unique_accounts": len(logins),
        "sampled": len(rows),
        "suggested_cutoff": cutoff,
        "results": rows,
    }


def write_preview(config):
    result = preview(config)
    output = config.with_name("tdm-lot-preview-report.json")
    temporary = output.with_suffix(".tmp")
    temporary.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(output)
    return result


def init_telegram(crd, *_):
    tg = crd.telegram
    if tg is None:
        return
    from telebot.types import InlineKeyboardButton, InlineKeyboardMarkup
    from Utils import cardinal_tools

    bot = tg.bot
    running = Lock()
    products_dir = Path("storage/products").resolve()

    def authorized(message, sender):
        plugin = crd.plugins.get(UUID)
        return (plugin is not None and plugin.enabled and message.chat.type == "private"
                and sender.id in tg.authorized_users)

    def lots():
        result = {}
        for title in crd.AD_CFG.sections():
            filename = crd.AD_CFG[title].get("productsFileName")
            if not filename or Path(filename).name != filename:
                continue
            path = products_dir / filename
            if path.resolve().parent != products_dir or not path.is_file():
                continue
            ref = hashlib.sha256(title.encode()).hexdigest()[:24]
            result[ref] = (title, path)
        return result

    def reply(chat, text, keyboard=None):
        bot.send_message(chat, html.escape(text), parse_mode="HTML", reply_markup=keyboard)

    def show_lots(chat, page):
        entries = sorted(lots().items(), key=lambda entry: entry[1][0].casefold())
        page = max(0, min(page, max(0, len(entries) - 1) // 8))
        keyboard = InlineKeyboardMarkup()
        for ref, (title, _) in entries[page * 8:(page + 1) * 8]:
            keyboard.add(InlineKeyboardButton(title[:55], callback_data="tdm-lots:lot:" + ref))
        if page:
            keyboard.add(InlineKeyboardButton("Назад", callback_data=f"tdm-lots:page:{page-1}"))
        if (page + 1) * 8 < len(entries):
            keyboard.add(InlineKeyboardButton("Дальше", callback_data=f"tdm-lots:page:{page+1}"))
        text = "Выбери запись автовыдачи для проверки." if entries else "Нет записей автовыдачи с доступным TXT."
        reply(chat, text, keyboard)

    def open_menu(message):
        if authorized(message, message.from_user):
            show_lots(message.chat.id, 0)

    def handle(call):
        if not authorized(call.message, call.from_user):
            bot.answer_callback_query(call.id)
            return
        _, action, value = call.data.split(":", 2)
        chat = call.message.chat.id
        bot.answer_callback_query(call.id)
        if action == "page" and value.isdecimal():
            show_lots(chat, int(value))
            return
        ref, _, product_format = value.partition("/")
        item = lots().get(ref)
        if item is None:
            reply(chat, "Запись автовыдачи изменилась. Открой /tdm_lots заново.")
            return
        title, path = item
        product_lock = cardinal_tools.get_products_file_lock(f"storage/products/{path.name}")
        if action == "lot":
            count = cardinal_tools.count_products(f"storage/products/{path.name}")
            keyboard = InlineKeyboardMarkup()
            keyboard.add(InlineKeyboardButton("Логин:пароль", callback_data=f"tdm-lots:run:{ref}/login-password"))
            keyboard.add(InlineKeyboardButton("Полный TXT", callback_data=f"tdm-lots:run:{ref}/full"))
            reply(chat, f"{title[:150]}\nФайл: {path.name}\nОстаток по Cardinal: {count}\nПроверка первых 10 уникальных аккаунтов. Выбери формат TXT.", keyboard)
            return
        if action != "run" or product_format not in ("login-password", "full"):
            return
        if not running.acquire(blocking=False):
            reply(chat, "Проверка уже выполняется.")
            return

        def run():
            try:
                result = preview_settings({"format": product_format, "products_path": str(path),
                                           "lot_id": ref, "use_telegram_selection": True}, product_lock)
                counts = dict.fromkeys(("missing_from_inventory", "awaiting_claim", "claimed_delivery_unverified", "unknown"), 0)
                detail = []
                for row in result["results"]:
                    counts[row["state"]] += 1
                    missing = (row.get("inventory") or {}).get("Missing") or []
                    if missing:
                        names = ", ".join(reward["Name"][:55] for reward in missing[:3])
                        detail.append(f"Строка {row['row']}: {names}")
                text = (f"{title[:150]} · {result['game']} · кампаний: {result['campaign_count']}\n"
                        f"Проверено: {result['sampled']} из {result['unique_accounts']}\n"
                        f"Награды отсутствуют или не готовы: {counts['missing_from_inventory']}\n"
                        f"Ждут получения: {counts['awaiting_claim']}\n"
                        f"Получено на Twitch, выдача в игре не проверена: {counts['claimed_delivery_unverified']}\n"
                        f"Нет данных: {counts['unknown']}\n"
                        "Дата снятия: неизвестна.")
                if detail:
                    text += "\n\n" + "\n".join(detail[:5])
                if result["products_changed"]:
                    text += "\n\nTXT изменился во время проверки. Запусти её заново."
                reply(chat, text)
            except error.HTTPError as exc:
                reply(chat, f"Оркестратор ответил HTTP {exc.code}. Проверь связь и сохрани выбор кампаний в его боте.")
            except (OSError, ValueError, KeyError, TypeError):
                reply(chat, "Проверка не запустилась. Проверь связь с оркестратором и формат TXT.")
            finally:
                running.release()

        Thread(target=run, daemon=True).start()
        reply(chat, "Проверка запущена. Результат появится в этом чате.")

    tg.msg_handler(open_menu, commands=["tdm_lots"])
    tg.cbq_handler(handle, lambda call: call.data.startswith("tdm-lots:"))
    crd.add_telegram_commands(UUID, [("tdm_lots", "Предпросмотр Drops для лота", True)])


BIND_TO_PRE_INIT = [init_telegram]


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=DESCRIPTION)
    cli.add_argument("config", type=Path)
    write_preview(cli.parse_args().config)
