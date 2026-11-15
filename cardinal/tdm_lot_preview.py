"""TDM inventory checks and guarded Cardinal delivery for configured lots."""
import argparse
import datetime as dt
import json
import logging
import hashlib
import html
import http.client
import socket
import os
import tempfile
import time
from pathlib import Path
from contextlib import nullcontext
import re
from threading import BoundedSemaphore, Lock, RLock, Thread
from urllib import error, parse, request

NAME = "TDM Drops lots"
VERSION = "0.4.2"
DESCRIPTION = "Checks Twitch Drops, guards Cardinal delivery and retires configured lots."
CREDITS = "TDM Light Orchestrator"
UUID = "b66cfbb9-9fe6-40e1-b9da-cf1d0e326542"
SETTINGS_PAGE = False
BIND_TO_DELETE = None

LOG = logging.getLogger(__name__)
URL = "http://127.0.0.1:8787/internal/lot-preview"
LOCAL_HTTP = request.build_opener(request.ProxyHandler({}))
LOGIN = re.compile(r"[a-zA-Z0-9_]+")
MANAGED_URL = "http://127.0.0.1:8787/internal/managed-lot"
DEADLINE = re.compile(r"\[\s*до\s+(\d{1,2})\.(\d{1,2})(?:\.(\d{4}))?\s*\]", re.IGNORECASE)
MSK = dt.timezone(dt.timedelta(hours=3))
SCAN_INTERVAL = 900


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
        url = URL
        if settings.get("lot_ref"):
            url += "?" + parse.urlencode({"lot": settings["lot_ref"]})
        selection_request = request.Request(url, headers={"Authorization": "Bearer " + token})
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


def managed_request(ref, operation=None, login=None, account_line=None, title=None, sample=None):
    token = os.environ.get("ORCHESTRATOR_LOT_PREVIEW_TOKEN", "")
    if len(token) < 32:
        raise ValueError("Orchestrator connection is not configured")
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
    if operation is None:
        req = request.Request(MANAGED_URL + "?" + parse.urlencode({"lot": ref}), headers=headers)
    else:
        data = {"Operation": operation, "Lot": ref, "Login": login}
        if account_line is not None:
            data["AccountLine"] = account_line
        if title is not None:
            data["Title"] = title
        if sample is not None:
            data["Sample"] = sample
        req = request.Request(MANAGED_URL, data=json.dumps(data).encode(), headers=headers)
    with open_orchestrator(req) as response:
        return json.load(response)


def title_deadline(title, now, previous=None):
    matches = DEADLINE.findall(title)
    if len(matches) > 1 or (not matches and re.search(r"\[\s*до\b", title, re.I)):
        raise ValueError("Укажите одну дату в названии в формате [ДО ДД.ММ] или [ДО ДД.ММ.ГГГГ].")
    if not matches:
        return None
    day, month, year = matches[0]
    marker = f"{int(day):02}.{int(month):02}.{year}"
    if previous and previous["marker"] == marker:
        return previous
    year_number = int(year) if year else now.year
    if not year and now.month == 12 and int(month) == 1:
        year_number += 1
    elif not year and now.month == 1 and int(month) == 12:
        year_number -= 1
    cutoff = dt.datetime(year_number, int(month), int(day), tzinfo=MSK)
    return {"marker": marker, "at": cutoff.isoformat()}


def product_rows(path, product_format):
    rows = {}
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        if not line.strip():
            continue
        fields = line.split(":", 1) if product_format == "login-password" else line.split(":")
        full = product_format == "full"
        if len(fields) != (6 if full else 2):
            raise ValueError(f"Некорректный формат TXT в строке {number}.")
        login = fields[int(full)].strip().lower()
        if not LOGIN.fullmatch(login) or login in rows or not all(field.strip() for field in fields):
            raise ValueError(f"Проверьте данные и дубликаты в строке {number}.")
        rows[login] = line
    return rows


def lot_stock_sample(path, blocked, sold, limit=5):
    sample = []
    seen = set()
    with path.open(encoding="utf-8-sig") as rows:
        for line in rows:
            fields = [field.strip() for field in line.rstrip("\r\n").split(":")]
            full = len(fields) == 6 and "@" in fields[0] and re.fullmatch(r"[0-9]{1,20}", fields[4])
            login = fields[1 if full else 0].lower()
            if len(fields) < 2 or len(login) > 64 or not LOGIN.fullmatch(login):
                continue
            if login in seen or login in blocked or sold_key(login) in sold:
                continue
            item = {"Login": login}
            if full and re.fullmatch(r"[!-~]{1,512}", fields[3]) and re.fullmatch(r"[!-~]{1,128}", fields[5]):
                item.update(OAuth=fields[3], UserID=fields[4], DeviceID=fields[5])
            sample.append(item)
            seen.add(login)
            if len(sample) == limit:
                break
    return sample


def atomic_text(path, text, mode=0o600):
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", newline="\n", dir=path.parent,
                                     prefix=".tdm-", delete=False) as file:
        temporary = Path(file.name)
        try:
            if os.name != "nt":
                os.fchmod(file.fileno(), mode)
            file.write(text)
            file.flush()
            os.fsync(file.fileno())
        except BaseException:
            temporary.unlink(missing_ok=True)
            raise
    try:
        temporary.replace(path)
        if os.name == "nt":
            return
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def sold_key(login):
    return hashlib.sha256(login.encode()).hexdigest()


def lot_code_button(button_type, ref):
    class CopyCodeButton(button_type):
        def to_dict(self):
            return {"text": self.text, "copy_text": {"text": ref}}

    return CopyCodeButton("📋 Скопировать код")


def same_lot_title(left, right):
    return DEADLINE.sub("", left).strip() == DEADLINE.sub("", right).strip()


class LotAutomation:
    def __init__(self, cardinal, tools, state_path=Path("storage/tdm-managed-lots.json")):
        self.cardinal = cardinal
        self.tools = tools
        self.path = state_path
        self.products = Path("storage/products").resolve()
        self.lock = RLock()
        self.funpay_lock = RLock()
        self.background_requests = BoundedSemaphore(4)
        self.workers = set()
        self.status = {}
        self.preferred = {}
        self.offer_checked = {}
        self.supported = False
        self.data = json.loads(self.path.read_text(encoding="utf-8")) if self.path.exists() else {}

    def update(self, ref, **changes):
        with self.lock:
            data = dict(self.data)
            data[ref] = dict(data.get(ref, {}), **changes)
            atomic_text(self.path, json.dumps(data, ensure_ascii=False))
            self.data = data

    def product_file(self, lot):
        filename = lot["file"]
        if self.cardinal.AD_CFG[lot["section"]].get("productsFileName") != filename:
            raise ValueError("У лота изменился файл автовыдачи.")
        if sum(self.cardinal.AD_CFG[name].get("productsFileName") == filename
               for name in self.cardinal.AD_CFG.sections()) != 1:
            raise ValueError("Этот TXT используется несколькими записями автовыдачи.")
        path = self.products / filename
        if path.is_symlink() or path.resolve().parent != self.products or not path.is_file():
            raise ValueError("Файл автовыдачи недоступен.")
        return path, self.tools.get_products_file_lock(f"storage/products/{filename}")

    def offer(self, lot):
        fields = self.cardinal.account.get_lot_fields(lot["id"])
        if (not fields.title_ru or fields.lot_id != lot["id"] or
                not any(same_lot_title(lot["section"], title) for title in (fields.title_ru, fields.title_en))):
            raise ValueError("Название или ID лота FunPay больше не совпадает с записью автовыдачи.")
        if fields.auto_delivery or fields.secrets:
            raise ValueError("Выключите встроенную автовыдачу FunPay для этого лота. Проверка работает с TXT Cardinal.")
        return fields

    def enable(self, ref, section, filename, offer_id, product_format, chat):
        if not self.supported:
            raise ValueError("Версия Cardinal не поддерживает проверку перед выдачей.")
        bundle = managed_request(ref)
        if not bundle.get("Rewards"):
            raise ValueError("Сначала сохраните состав лота в боте оркестратора.")
        lot = {"section": section, "file": filename, "id": offer_id, "format": product_format, "chat": chat}
        with self.funpay_lock, self.lock:
            if ref in self.data:
                raise ValueError("Автоматизация уже настроена для этого лота.")
            if any(item["id"] == offer_id or item["file"] == filename for item in self.data.values()):
                raise ValueError("ID или TXT уже привязан к другому лоту.")
            if "$product" not in self.cardinal.AD_CFG[section].get("response", ""):
                raise ValueError("Добавьте $product в шаблон автовыдачи Cardinal.")
            fields = self.offer(lot)
            path, locked = self.product_file(lot)
            with locked:
                product_rows(path, product_format)
            deadline = title_deadline(fields.title_ru, dt.datetime.now(MSK))
            self.update(ref, **lot, deadline=deadline, retired=None, inactive_since=None,
                        orders={}, pending={}, sold=[])
        self.start_worker(ref)
        return bundle

    def notify(self, lot, message):
        tg = self.cardinal.telegram
        try:
            if tg is not None and lot["chat"] in tg.authorized_users:
                tg.bot.send_message(lot["chat"], html.escape(message), parse_mode="HTML")
        except Exception:
            LOG.warning("TDM: Telegram notification failed for listing %s", lot["id"])

    def check(self, ref, lot, login, line, foreground=False):
        try:
            with nullcontext() if foreground else self.background_requests:
                return managed_request(ref, "inspect", login, line if lot["format"] == "full" else None)["state"]
        except (OSError, ValueError, KeyError, TypeError):
            return "inventory_unknown"

    def start_worker(self, ref):
        with self.lock:
            if ref in self.workers:
                return
            self.workers.add(ref)
        Thread(target=self.monitor, args=(ref,), daemon=True, name=f"TDM lot {ref}").start()

    def monitor(self, ref):
        cache = {}
        bundle = None
        bundle_checked = 0
        checked_in_sweep = set()
        next_sweep = 0
        next_release = 0
        last_error = None
        while True:
            if not self.cardinal.plugins[UUID].enabled:
                time.sleep(5)
                continue
            lot = self.data[ref]
            try:
                self.finish_sales(ref, lot)
                lot = self.refresh_offer(ref)
                if lot["retired"]:
                    if time.monotonic() >= next_release:
                        next_release = time.monotonic() + 60
                        self.retire_stock(ref, lot)
                else:
                    if time.monotonic() >= bundle_checked:
                        current_bundle = managed_request(ref)
                        if current_bundle != bundle:
                            bundle = current_bundle
                            cache.clear()
                            checked_in_sweep.clear()
                            next_sweep = 0
                        bundle_checked = time.monotonic() + 60
                    path, locked = self.product_file(lot)
                    with locked:
                        rows = product_rows(path, lot["format"])
                    blocked = self.blocked_logins(lot)
                    rows = {login: line for login, line in rows.items()
                            if login not in blocked and sold_key(login) not in lot["sold"]}
                    stamp = time.monotonic()
                    cache = {login: item for login, item in cache.items()
                             if login in rows and item[0] == sold_key(rows[login])}
                    checked_in_sweep.intersection_update(cache)
                    if stamp >= next_sweep:
                        for login, line in rows.items():
                            if login not in checked_in_sweep:
                                cache[login] = (sold_key(line), self.check(ref, lot, login, line))
                                checked_in_sweep.add(login)
                                break
                    states = [item[1] for item in cache.values()]
                    self.preferred[ref] = {login for login, item in cache.items() if item[1] == "ready"}
                    ready = states.count("ready")
                    missing = states.count("missing")
                    self.status[ref] = (len(rows), ready, missing, len(states) - ready - missing)
                    completed = stamp >= next_sweep and len(checked_in_sweep) == len(rows)
                    if not rows or (completed and missing == len(rows)):
                        if rows and bundle != managed_request(ref):
                            bundle_checked = 0
                            continue
                        with self.funpay_lock, self.lock, locked:
                            current = self.data[ref]
                            actual = product_rows(path, lot["format"])
                            actual = {login: line for login, line in actual.items() if login not in self.blocked_logins(current)
                                      and sold_key(login) not in current["sold"]}
                            if actual == rows and not current["retired"]:
                                self.update(ref, retired="подтверждено отсутствие нужных наград" if rows else "остаток закончился")
                    if completed:
                        checked_in_sweep.clear()
                        next_sweep = stamp + SCAN_INTERVAL
                last_error = None
            except Exception as exc:
                reason = type(exc).__name__
                if last_error != reason:
                    LOG.warning("TDM: listing %s monitoring failed (%s)", lot["id"], reason)
                    self.notify(lot, f"⚠️ Лот #{lot['id']} · проверка приостановлена. Проверьте настройки лота, TXT и связь с сервисами.")
                    last_error = reason
                time.sleep(30)
            time.sleep(1)

    def refresh_offer(self, ref):
        now = dt.datetime.now(MSK)
        with self.funpay_lock, self.lock:
            lot = self.data[ref]
            deadline = lot["deadline"]
            due = deadline is not None and now >= dt.datetime.fromisoformat(deadline["at"])
            # Remote reads are rate-limited separately from the Twitch scans.
            last = self.offer_checked
            if not due and not lot["retired"] and time.monotonic() - last.get(ref, -60) < 60:
                return lot
            if lot["inactive_since"] and time.monotonic() - last.get(ref, -60) < 60:
                return lot
            fields = self.offer(lot)
            deadline = title_deadline(fields.title_ru, now, deadline)
            if deadline != lot["deadline"]:
                self.update(ref, deadline=deadline)
                lot = self.data[ref]
            if deadline and now >= dt.datetime.fromisoformat(deadline["at"]) and not lot["retired"]:
                self.update(ref, retired="наступил срок из названия")
                lot = self.data[ref]
            if lot["retired"]:
                if fields.active:
                    fields.active = False
                    self.cardinal.account.save_lot(fields)
                    if self.offer(lot).active:
                        raise RuntimeError("FunPay still reports an active listing")
                if not lot["inactive_since"]:
                    self.update(ref, inactive_since=now.isoformat())
                    lot = self.data[ref]
                    self.notify(lot, f"⏸ Лот #{lot['id']} снят · {lot['retired']}.")
            last[ref] = time.monotonic()
            return lot

    @staticmethod
    def blocked_logins(lot):
        return {login for order in lot["orders"].values() for login in order["logins"]} | set(lot["pending"])

    def open_orders(self, lot):
        cursor = None
        seen = set()
        while True:
            cursor, orders = self.cardinal.account.get_sells(start_from=cursor, state="paid",
                                                             include_closed=False, include_refunded=False)
            # Leave unrelated known orders alone. An unmatched order may be
            # for the managed lot under its previous description.
            for order in orders:
                candidates = [name for name in self.cardinal.AD_CFG.sections()
                              if DEADLINE.sub("", name).strip() in DEADLINE.sub("", order.description)]
                if len(candidates) != 1 or candidates[0] == lot["section"]:
                    return True
            if not cursor:
                return False
            if cursor in seen:
                raise RuntimeError("FunPay returned an incomplete order list")
            seen.add(cursor)

    def retire_stock(self, ref, lot):
        inactive = lot["inactive_since"]
        if not inactive or dt.datetime.now(MSK) - dt.datetime.fromisoformat(inactive) < dt.timedelta(minutes=2):
            return
        with self.funpay_lock, self.lock:
            lot = self.data[ref]
            if self.offer(lot).active or self.open_orders(lot):
                return
            path, locked = self.product_file(lot)
            with locked:
                rows = product_rows(path, lot["format"])
                order_logins = {login for item in lot["orders"].values() for login in item["logins"]}
                pending = dict(lot["pending"])
                for login in rows:
                    if login not in order_logins and sold_key(login) not in lot["sold"]:
                        pending.setdefault(login, False)
                if pending != lot["pending"]:
                    self.update(ref, pending=pending)
        # Register accounts first. Do not hold the stock lock across a Twitch read.
        transferable = {}
        for login, line in rows.items():
            if login not in pending:
                continue
            if pending[login] or self.check(ref, lot, login, line) in {
                    "ready", "missing", "claimed_unverified", "claim_unverified"}:
                transferable[login] = line
        if transferable:
            with self.funpay_lock, self.lock, locked:
                if self.offer(lot).active or self.open_orders(lot):
                    return
                current = product_rows(path, lot["format"])
                matched = {login: line for login, line in transferable.items() if current.get(login) == line}
                if matched:
                    # Keep the intent until the original row is absent. A restart
                    # after the atomic write can resend the idempotent release.
                    atomic_text(path, "\n".join(line for login, line in current.items() if login not in matched))
        with self.lock, locked:
            current = product_rows(path, lot["format"])
            pending = self.data[ref]["pending"]
            detached = dict(pending, **{login: True for login in pending if login not in current})
            if pending != detached:
                self.update(ref, pending=detached)
        returned = []
        for login, detached in self.data[ref]["pending"].items():
            if not detached:
                continue
            try:
                with self.background_requests:
                    status = managed_request(ref, "release", login)["state"]
            except (OSError, ValueError, KeyError, TypeError):
                continue
            if status == "returned":
                returned.append(login)
        if returned:
            with self.lock:
                current = self.data[ref]
                pending = {key: value for key, value in current["pending"].items() if key not in returned}
                self.update(ref, pending=pending, returned=current.get("returned", 0) + len(returned))
            self.notify(lot, f"♻️ Лот #{lot['id']} · в свободный пул возвращено {len(returned)}. Ожидают проверки: {len(pending)}.")

    def guarded_delivery(self, event, ref, lot):
        from handlers import deliver_product_handler

        if not ref:
            deliver_product_handler(self.cardinal, event)
            return
        section = self.cardinal.AD_CFG[lot["section"]]
        if not self.cardinal.autodelivery_enabled or section.getboolean("disable"):
            return
        if self.cardinal.bl_delivery_enabled and event.order.buyer_username in self.cardinal.blacklist:
            return
        order_id = event.order.id
        if order_id == "ADTEST":
            raise ValueError("Для этого лота используйте проверку в /tdm_lots. Тестовый заказ не расходует TXT.")
        if getattr(event, "lot_id", None) != lot["id"]:
            raise ValueError("Cardinal не подтвердил ID лота для этого заказа.")
        multi = self.cardinal.multidelivery_enabled and not section.getboolean("disableMultiDelivery")
        amount = (event.order.amount or 1) if multi else 1
        if not 1 <= amount <= 20:
            raise ValueError("Количество в заказе требует ручной выдачи.")
        with self.lock:
            if order_id in self.data[ref]["orders"]:
                return
        lot = self.refresh_offer(ref)
        if lot["retired"]:
            raise ValueError("Лот снят. Заказ требует внимания продавца.")
        path, locked = self.product_file(lot)
        bundle = managed_request(ref)
        with self.lock, locked:
            current = self.data[ref]
            rows = product_rows(path, current["format"])
            blocked = self.blocked_logins(current)
            sold = set(current["sold"])
        selected = {}
        preferred = self.preferred.get(ref, set())
        started = time.monotonic()
        for login in sorted(rows, key=lambda key: key not in preferred):
            line = rows[login]
            if login in blocked or sold_key(login) in sold:
                continue
            if time.monotonic() - started > 40:
                break
            if self.check(ref, lot, login, line, foreground=True) == "ready":
                selected[login] = line
                if len(selected) == amount:
                    break
        if len(selected) != amount:
            raise ValueError("Свежая проверка не подтвердила достаточно аккаунтов. Товар остался в TXT.")
        if bundle != managed_request(ref):
            raise ValueError("Состав лота изменился во время проверки.")
        if "$product" not in section["response"]:
            raise ValueError("В сообщении автовыдачи отсутствует $product.")
        text = self.tools.format_order_text(section["response"], event.order)
        text = text.replace("$product", "\n".join(selected.values()))
        # One outbound message prevents a partially sent order from being
        # mistaken for a complete delivery or returned to stock.
        entities = self.cardinal.parse_message_entities(text)
        if len(text) > 3500 or len(text.splitlines()) > 19 or len(entities) != 1 or not isinstance(entities[0], str):
            raise ValueError("Шаблон или размер выдачи требуют ручной отправки заказа.")
        with self.funpay_lock:
            lot = self.refresh_offer(ref)
            with self.lock, locked:
                current = self.data[ref]
                if lot["retired"] or order_id in current["orders"]:
                    return
                rows_now = product_rows(path, current["format"])
                blocked_now = self.blocked_logins(current)
                sold_now = set(current["sold"])
                if any(rows_now.get(login) != line or login in blocked_now or sold_key(login) in sold_now
                       for login, line in selected.items()):
                    raise ValueError("TXT изменился во время проверки. Нужно проверить заказ заново.")
                orders = dict(current["orders"], **{order_id: {"sent": False, "logins": list(selected)}})
                self.update(ref, orders=orders)
                atomic_text(path, "\n".join(line for login, line in rows_now.items() if login not in selected))
            chat = self.cardinal.account.get_chat_by_name(event.order.buyer_username)
            result = self.cardinal.send_message(chat.id if chat else event.order.chat_id,
                                                text, event.order.buyer_username, attempts=1, watermark=False)
            if not result or len(result) != 1 or result[0] is None:
                raise ValueError("Выдача не подтверждена. Аккаунты зарезервированы за заказом, проверьте чат FunPay.")
            with self.lock:
                current = self.data[ref]
                orders = dict(current["orders"], **{order_id: {"sent": True, "logins": list(selected)}})
                self.update(ref, orders=orders, sold=current["sold"] + [sold_key(login) for login in selected])
        event.delivered = True
        event.goods_delivered = amount
        event.goods_left = len(rows_now) - amount
        self.notify(lot, f"✅ Лот #{lot['id']} · заказ #{order_id} · отправлено аккаунтов: {amount}.")

    def install(self):
        import handlers
        original = handlers.deliver_product_handler
        positions = [i for i, handler in enumerate(self.cardinal.new_order_handlers) if handler is original]
        if len(positions) != 1:
            LOG.error("TDM: compatible Cardinal delivery handler not found")
            return
        apply_state = handlers.update_lot_state

        def state_guard(cardinal, lot, task):
            with self.funpay_lock:
                if self.cardinal is cardinal and any(item["id"] == lot.id for item in self.data.values()):
                    return False
                return apply_state(cardinal, lot, task)

        def delivery_guard(cardinal, event, *_):
            cfg = getattr(event, "config_section_obj", None)
            filename = cfg.get("productsFileName") if cfg is not None else None
            matches = [(ref, lot) for ref, lot in self.data.items()
                       if lot["id"] == getattr(event, "lot_id", None) or
                       (cfg is not None and (lot["section"] == cfg.name or lot["file"] == filename)) or
                       DEADLINE.sub("", lot["section"]).strip() in DEADLINE.sub("", event.order.description)]
            if not matches:
                return original(cardinal, event)
            ref, lot = matches[0]
            try:
                if len(matches) > 1:
                    raise ValueError("Заказ совпал с несколькими управляемыми лотами.")
                if not self.cardinal.plugins[UUID].enabled:
                    raise ValueError("Автоматизация лотов отключена. Заказ требует внимания продавца.")
                self.guarded_delivery(event, ref, lot)
            except Exception as exc:
                event.error = 1
                event.error_text = "Автовыдача приостановлена. Проверьте заказ в Cardinal."
                message = str(exc) if isinstance(exc, ValueError) else "Проверка или выдача не подтверждена. Проверьте заказ в Cardinal."
                self.notify(lot, f"⚠️ Заказ #{event.order.id}\n{message}")
                LOG.warning("TDM: order %s held (%s)", event.order.id, type(exc).__name__)

        delivery_guard.plugin_uuid = None
        self.cardinal.new_order_handlers[positions[0]] = delivery_guard
        handlers.update_lot_state = state_guard
        self.supported = True
        for ref in self.data:
            self.start_worker(ref)

    def finish_sales(self, ref, lot):
        for order_id, order in lot["orders"].items():
            if not order["sent"] or not order["logins"]:
                continue
            try:
                acknowledged = [managed_request(ref, "sold", login)["state"] == "sold" for login in order["logins"]]
            except (OSError, ValueError, KeyError, TypeError):
                continue
            if all(acknowledged):
                with self.lock:
                    current = self.data[ref]
                    orders = dict(current["orders"], **{order_id: {"sent": True, "logins": []}})
                    self.update(ref, orders=orders)


def init_telegram(crd, *_):
    tg = crd.telegram
    from Utils import cardinal_tools
    automation = LotAutomation(crd, cardinal_tools)
    crd.tdm_lot_automation = automation
    if tg is None:
        return
    from telebot.types import InlineKeyboardButton, InlineKeyboardMarkup

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

    def reply(chat, text, keyboard=None, lot_code=None):
        title, _, body = text.partition("\n\n")
        formatted = html.escape(text) if lot_code is None else (
            f"{html.escape(title)}\n\nКод лота · <code>{html.escape(lot_code)}</code>\n{html.escape(body)}")
        bot.send_message(chat, formatted, parse_mode="HTML", reply_markup=keyboard)

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
        text = "🏷 Лоты Twitch Drops\n\nВыберите запись автовыдачи Cardinal." if entries else "Нет записей автовыдачи с доступным TXT."
        reply(chat, text, keyboard)

    def open_menu(message):
        if authorized(message, message.from_user):
            show_lots(message.chat.id, 0)

    def enable_lot(chat, ref, item, offer_id, product_format):
        title, path = item
        try:
            bundle = automation.enable(ref, title, path.name, offer_id, product_format, chat)
        except ValueError as exc:
            reply(chat, f"⚠️ {exc}")
        except error.HTTPError as exc:
            reply(chat, f"⚠️ Оркестратор ответил HTTP {exc.code}. Проверьте связь и состав лота.")
        except Exception as exc:
            LOG.warning("TDM: listing %s could not be enabled (%s)", offer_id, type(exc).__name__)
            reply(chat, "⚠️ Проверьте ID лота, доступность TXT и связь с оркестратором.")
        else:
            reply(chat, f"✅ Лот #{offer_id} подключён.\n{bundle['Game']} · наград в наборе: {len(bundle['Rewards'])}.\n"
                        "Проверка остатка и автовыдачи работают в фоне.")

    def enable_command(message):
        if not authorized(message, message.from_user):
            return
        fields = message.text.split()
        if len(fields) != 4 or not re.fullmatch(r"[0-9]{1,12}", fields[2]) or fields[3] not in ("login-password", "full"):
            reply(message.chat.id, "Формат команды\n/tdm_lot_enable код_лота ID_FunPay login-password\nДля полного TXT используйте full.")
            return
        item = lots().get(fields[1])
        if item is None:
            reply(message.chat.id, "Запись автовыдачи не найдена. Откройте /tdm_lots и скопируйте её код.")
            return
        enable_lot(message.chat.id, fields[1], item, int(fields[2]), fields[3])

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
            keyboard.add(InlineKeyboardButton("🔎 Проверить логин:пароль", callback_data=f"tdm-lots:run:{ref}/login-password"))
            keyboard.add(InlineKeyboardButton("🔎 Проверить полный TXT", callback_data=f"tdm-lots:run:{ref}/full"))
            text = f"{title[:150]}\n\nФайл · {path.name}\nОстаток в Cardinal · {count}"
            lot = automation.data.get(ref)
            if lot is None:
                keyboard.add(InlineKeyboardButton("⚙️ Настроить автоматизацию", callback_data=f"tdm-lots:setup:{ref}"))
                text += "\n\nРучная проверка охватывает первые 10 аккаунтов."
            else:
                text += f"\n\n⚙️ Лот FunPay #{lot['id']} · автоматизация включена."
                if lot["deadline"]:
                    cutoff = dt.datetime.fromisoformat(lot["deadline"]["at"])
                    text += f"\n📅 Снятие по дате · {cutoff:%d.%m.%Y %H:%M} МСК"
                if lot["retired"]:
                    text += f"\n⏸ {lot['retired'].capitalize()}." + (" Снят с продажи." if lot["inactive_since"] else " Ожидается подтверждение FunPay.")
                    text += f"\n♻️ Возвращено · {lot.get('returned', 0)} · ожидают проверки · {len(lot['pending'])}"
                elif ref in automation.status:
                    total, ready, missing, unknown = automation.status[ref]
                    text += f"\n📊 Состав подтверждён · {ready}/{total}\nНаграды отсутствуют · {missing}\nПодтверждение недоступно · {unknown}"
                    text += f"\nОжидают первого замера · {total - ready - missing - unknown}"
                else:
                    text += "\nОжидание первого замера."
                unresolved = sum(not order["sent"] for order in lot["orders"].values())
                if unresolved:
                    text += f"\n⚠️ Заказы с неподтверждённой отправкой · {unresolved}"
                keyboard.add(InlineKeyboardButton("🔄 Обновить", callback_data=f"tdm-lots:lot:{ref}"))
            keyboard.add(InlineKeyboardButton("📋 Настроить состав", callback_data=f"tdm-lots:edit:{ref}"))
            keyboard.add(InlineKeyboardButton("📅 Сроки и состав", callback_data=f"tdm-lots:bundle:{ref}"))
            keyboard.add(lot_code_button(InlineKeyboardButton, ref))
            keyboard.add(InlineKeyboardButton("⬅️ К лотам", callback_data="tdm-lots:page:0"))
            reply(chat, text, keyboard, lot_code=ref)
            return
        if action == "setup":
            profile = crd.tg_profile or crd.profile
            candidates = [offer for offer in profile.get_sorted_lots(1).values()
                          if same_lot_title(title, offer.description)] if profile is not None else []
            if len(candidates) != 1:
                reply(chat, f"Сохраните состав лота в боте оркестратора по коду {ref}.\n\n"
                            "Укажите ID из ссылки FunPay offer?id=… и формат файла.\n"
                            f"/tdm_lot_enable {ref} ID login-password\nДля полного TXT замените login-password на full.")
                return
            offer_id = candidates[0].id
            keyboard = InlineKeyboardMarkup()
            for short, label in (("lp", "Логин:пароль"), ("txt", "Полный TXT")):
                keyboard.add(InlineKeyboardButton(label, callback_data=f"tdm-lots:enable:{ref}/{offer_id}/{short}"))
            keyboard.add(InlineKeyboardButton("⬅️ Отмена", callback_data=f"tdm-lots:lot:{ref}"))
            reply(chat, f"⚙️ Автоматизация лота #{offer_id}\n\nСначала сохраните состав в боте оркестратора по коду {ref}.\n"
                        "Выбор формата включает проверку перед выдачей и автоматическое снятие лота.", keyboard)
            return
        if action == "edit":
            keyboard = InlineKeyboardMarkup()
            keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
            try:
                with automation.lock, product_lock:
                    lot = automation.data.get(ref)
                    blocked = automation.blocked_logins(lot) if lot else set()
                    sold = set(lot["sold"]) if lot else set()
                    sample = lot_stock_sample(path, blocked, sold)
                if managed_request(ref, "register", title=title, sample=sample).get("state") != "registered":
                    raise ValueError("Registration was not acknowledged")
            except (OSError, ValueError, TypeError):
                reply(chat, "Не удалось передать лот. Проверьте связь с оркестратором и его версию.", keyboard)
                return
            reply(chat, "✅ Лот появился в боте оркестратора. Откройте «Лоты → Состав лота» и выберите его по названию."
                        " Для поиска в инвентаре оркестратор сам возьмёт аккаунты из этого лота.", keyboard)
            return
        if action == "bundle":
            keyboard = InlineKeyboardMarkup()
            keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
            try:
                bundle = managed_request(ref)
            except (OSError, ValueError, KeyError, TypeError):
                reply(chat, "Состав лота недоступен. Проверьте связь и сохраните набор в оркестраторе.", keyboard)
                return
            rewards = bundle["Rewards"]
            text = f"🏷 {bundle['Game']}\nКампаний: {len({reward['Campaign'] for reward in rewards})} · наград: {len(rewards)}"
            text += "\n\n" + "\n".join(f"• {reward['Name'][:80]}" for reward in rewards[:12])
            if len(rewards) > 12:
                text += f"\n… и ещё {len(rewards) - 12}"
            if bundle.get("SuggestedCutoff"):
                suggested = dt.datetime.fromisoformat(bundle["SuggestedCutoff"].replace("Z", "+00:00")).astimezone(MSK)
                text += f"\n\n📅 Самый ранний подтверждённый срок · {suggested:%d.%m.%Y %H:%M} МСК."
                text += f"\nДля снятия в 00:00 можно указать [ДО {suggested:%d.%m.%Y}]."
                text += "\n" + "\n".join(bundle.get("DeadlineSources", [])[:3])
            else:
                text += "\n\n📅 Точный срок известен не для всех кампаний. Укажите дату в названии вручную, если она подтверждена."
            reply(chat, text, keyboard)
            return
        if action == "enable":
            offer_id, _, short = product_format.partition("/")
            if offer_id.isdecimal() and short in ("lp", "txt"):
                enable_lot(chat, ref, item, int(offer_id), "full" if short == "txt" else "login-password")
            return
        if action != "run" or product_format not in ("login-password", "full"):
            return
        if not running.acquire(blocking=False):
            reply(chat, "Проверка уже выполняется.")
            return

        def run():
            try:
                try:
                    bundle = managed_request(ref)
                except error.HTTPError as exc:
                    if exc.code != 404:
                        raise
                else:
                    with product_lock:
                        rows = product_rows(path, product_format)
                    states = []
                    for login, line in list(rows.items())[:10]:
                        try:
                            result = managed_request(ref, "preview", login, line if product_format == "full" else None)
                            states.append(result["state"])
                        except (OSError, ValueError, KeyError, TypeError):
                            states.append("inventory_unknown")
                    ready, missing = states.count("ready"), states.count("missing")
                    reply(chat, f"🔎 {bundle['Game']} · наград в наборе: {len(bundle['Rewards'])}\n\n"
                                f"Проверено · {len(states)} из {len(rows)}\n"
                                f"Набор подтверждён Twitch · {ready}\nНужные награды отсутствуют · {missing}\n"
                                f"Данных для вывода недостаточно · {len(states) - ready - missing}")
                    return
                result = preview_settings({"format": product_format, "products_path": str(path),
                                           "lot_id": ref, "lot_ref": ref, "use_telegram_selection": True}, product_lock)
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
                reply(chat, f"Оркестратор ответил HTTP {exc.code}. Проверь связь и сохрани привязку кампаний для кода этого лота в боте оркестратора.")
            except (OSError, ValueError, KeyError, TypeError):
                reply(chat, "Проверка не запустилась. Проверь связь с оркестратором и формат TXT.")
            finally:
                running.release()

        Thread(target=run, daemon=True).start()
        reply(chat, "Проверка запущена. Результат появится в этом чате.")

    tg.msg_handler(open_menu, commands=["tdm_lots"])
    tg.msg_handler(enable_command, commands=["tdm_lot_enable"])
    tg.cbq_handler(handle, lambda call: call.data.startswith("tdm-lots:"))
    crd.add_telegram_commands(UUID, [("tdm_lots", "Лоты Twitch Drops", True)])


def start_automation(crd, *_):
    runtime = getattr(crd, "tdm_lot_automation", None)
    if runtime is not None:
        runtime.install()


BIND_TO_PRE_INIT = [init_telegram]
BIND_TO_POST_INIT = [start_automation]


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=DESCRIPTION)
    cli.add_argument("config", type=Path)
    write_preview(cli.parse_args().config)
