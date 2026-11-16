"""TDM inventory checks and guarded Cardinal delivery for configured lots."""
import argparse
import datetime as dt
import json
import logging
import hashlib
import html
import http.client
import io
import socket
import os
import stat
import tempfile
import time
from pathlib import Path
from contextlib import nullcontext
import re
from threading import BoundedSemaphore, Lock, RLock, Thread, local
from urllib import error, parse, request

NAME = "TDM Drops lots"
VERSION = "0.4.6"
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
NATIVE_FLAGS = ("disable", "disableAutoRestore", "disableAutoDisable")


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


def diagnostic_request(token, use_socket, ref, sample=None):
    connection_type = UnixHTTPConnection if use_socket else http.client.HTTPConnection
    connection = connection_type("127.0.0.1", 8787, timeout=13)
    payload = {"Operation": "diagnose", "Lot": ref}
    if sample:
        payload["Sample"] = sample
    try:
        connection.request("POST", "/internal/managed-lot", json.dumps(payload), {
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        })
        response = connection.getresponse()
        if response.status != 200:
            return response.status, None
        result = json.loads(response.read(4096))
        state = result.get("state") if isinstance(result, dict) else None
        return response.status, state if isinstance(state, str) else None
    finally:
        connection.close()


def orchestrator_diagnostics(ref=None, sample=None, sample_unreadable=False):
    socket_path = os.environ.get("ORCHESTRATOR_CARDINAL_SOCKET")
    if socket_path:
        try:
            mode = Path(socket_path).stat().st_mode
        except FileNotFoundError:
            return "❌ Сокет · не найден в контейнере Cardinal. Проверьте монтирование каталога."
        except OSError:
            return "❌ Сокет · Cardinal не может проверить путь. Проверьте доступ к каталогу."
        if not stat.S_ISSOCK(mode):
            return "❌ Сокет · по заданному пути находится другой тип файла. Проверьте путь."
        lines = ["✅ Сокет · файл виден Cardinal"]
    else:
        lines = ["ℹ️ Сокет не задан · проверяется локальный HTTP"]

    token = os.environ.get("ORCHESTRATOR_LOT_PREVIEW_TOKEN", "")
    if len(token) < 32:
        lines.append("❌ Токен · отсутствует или короче 32 символов. Проверьте файл окружения Cardinal.")
        return "\n".join(lines)

    try:
        status, state = diagnostic_request(token, bool(socket_path), "0" * 24)
    except PermissionError:
        lines.append("❌ Подключение · нет доступа к сокету. Проверьте права и группу Cardinal.")
    except (OSError, http.client.HTTPException, ValueError):
        lines.append("❌ Подключение · оркестратор не вернул читаемый ответ. Проверьте запуск контейнера.")
    else:
        if status in (401, 403, 404):
            lines.append("❌ Доступ · запрос отклонён. Проверьте совпадение общего токена и путь к оркестратору.")
        elif status == 400:
            lines.append("ℹ️ Токен принят. Обновите оркестратор до версии с диагностикой.")
        elif status != 200:
            lines.append(f"❌ Оркестратор · вернул HTTP {status}.")
        elif state != "orchestrator_ok":
            lines.append("❌ Оркестратор · ответ диагностики не распознан. Проверьте версии.")
        else:
            lines.extend(("✅ Токен · принят", "✅ Оркестратор · отвечает"))
            if ref is None:
                lines.append("ℹ️ Для проверки Twitch откройте диагностику в карточке лота.")
            elif sample_unreadable:
                lines.append("⚠️ Twitch · товарный TXT не удалось прочитать.")
            elif not sample:
                lines.append("ℹ️ Twitch · в TXT нет доступного аккаунта для проверки.")
            else:
                try:
                    _, state = diagnostic_request(token, bool(socket_path), ref, sample)
                except (OSError, http.client.HTTPException, ValueError):
                    state = None
                lines.append({
                    "twitch_ok": "✅ Twitch · инвентарь тестового аккаунта доступен",
                    "twitch_unavailable": "⚠️ Twitch · оркестратор не получил инвентарь тестового аккаунта. Проверьте авторизацию аккаунта или повторите позже.",
                    "twitch_no_account": "⚠️ Twitch · нет подходящих данных для проверки выбранного аккаунта.",
                }.get(state, "⚠️ Twitch · проверка не завершилась. Повторите позже."))
    return "\n".join(lines)


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


def product_rows(path, product_format, content=None):
    rows = {}
    if content is None:
        content = path.read_text(encoding="utf-8-sig")
    for number, line in enumerate(content.splitlines(), 1):
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


def order_state(order):
    return getattr(getattr(order, "status", None), "name", None)


def payment_status_text(payment):
    return {
        "PAID": "💳 FunPay · заказ оплачен.",
        "CLOSED": "💳 FunPay · заказ закрыт.",
        "REFUNDED": "💳 FunPay · заказ возвращён.",
        "PARTIALLY_REFUNDED": "💳 FunPay · частичный возврат.",
        "UNPAID": "💳 FunPay · заказ не оплачен.",
    }.get(payment, "💳 FunPay · статус оплаты не подтверждён.")


def stock_text(rows):
    return "\n".join(rows.values())


def stock_digest(rows):
    return sold_key(stock_text(rows))


def delivery_text_hash(text):
    if not isinstance(text, str) or not text.strip():
        return None
    return sold_key(text.replace("\r\n", "\n").strip())


def stock_identity(path):
    stat = path.stat()
    return [stat.st_dev, stat.st_ino]


def bundle_digest(bundle):
    return sold_key(json.dumps(bundle, sort_keys=True, ensure_ascii=False))


class LotAutomation:
    def __init__(self, cardinal, tools, state_path=Path("storage/tdm-managed-lots.json"),
                 native_config=Path("configs/auto_delivery.cfg")):
        self.cardinal = cardinal
        self.tools = tools
        self.path = state_path
        self.native_config = native_config
        self.products = Path("storage/products").resolve()
        self.lock = RLock()
        self.funpay_lock = RLock()
        self.notice_flush_lock = Lock()
        self.confirmed_notices = set()
        self.background_requests = BoundedSemaphore(4)
        self.workers = {}
        self.lot_operations = {}
        self.status = {}
        self.preferred = {}
        self.offer_checked = {}
        self.orders_checked = {}
        self.native_dirty = set()
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
            if self.cardinal.AD_CFG[section].getboolean("disable", fallback=False):
                raise ValueError("В записи Cardinal отключена автовыдача. Включите её перед подключением лота.")
            if "$product" not in self.cardinal.AD_CFG[section].get("response", ""):
                raise ValueError("Добавьте $product в шаблон автовыдачи Cardinal.")
            fields = self.offer(lot)
            path, locked = self.product_file(lot)
            with locked:
                product_rows(path, product_format)
            deadline = title_deadline(fields.title_ru, dt.datetime.now(MSK))
            section_settings = self.cardinal.AD_CFG[section]
            lot["native_settings"] = {key: section_settings.get(key) for key in NATIVE_FLAGS}
            self.protect_native_stock(lot)
            self.update(ref, **lot, deadline=deadline, retired=None, inactive_since=None,
                        orders={}, pending={}, sold=[], delivery_since=dt.datetime.now(dt.timezone.utc).isoformat())
        self.start_worker(ref)
        return bundle

    def protect_native_stock(self, lot):
        # Persist the native denial so a failed plugin import cannot restore unguarded sends.
        config = self.cardinal.AD_CFG
        section = config[lot["section"]]
        if section.get("productsFileName") != lot["file"]:
            raise ValueError("Изменился TXT управляемого лота.")
        if lot["section"] not in self.native_dirty and all(section.get(key) == "1" for key in NATIVE_FLAGS):
            return
        for key in NATIVE_FLAGS:
            section[key] = "1"
        self.save_native_config(lot["section"])

    def save_native_config(self, section):
        buffer = io.StringIO()
        self.cardinal.AD_CFG.write(buffer)
        self.native_dirty.add(section)
        atomic_text(self.native_config, buffer.getvalue())
        self.native_dirty.discard(section)

    def lot_operation(self, ref):
        with self.lock:
            return self.lot_operations.setdefault(ref, RLock())

    def detach(self, ref, *, expected_generation=None):
        with self.lot_operation(ref), self.funpay_lock, self.lock:
            lot = self.data.get(ref)
            if expected_generation is not None and (lot is None or (lot["id"], lot.get("delivery_since")) != expected_generation):
                raise ValueError("Подключение лота изменилось. Откройте карточку и подтвердите отвязку заново.")
            if lot is None:
                return
            if (lot["pending"] or lot.get("retirement_detachment") or lot.get("retirement_review")
                    or any(order["logins"] for order in lot["orders"].values())):
                raise ValueError("Сначала дождитесь сверки резервов и завершения возврата товара.")
            path, locked = self.product_file(lot)
            offer = self.offer(lot)
            if offer.active:
                raise ValueError("Сначала скройте лот на FunPay, чтобы во время передачи не поступали новые заказы.")
            reason = self.open_orders(lot, offer)
            if reason:
                raise ValueError(reason)
            with locked:
                rows = product_rows(path, lot["format"])
                sold = set(lot["sold"])
                if any(sold_key(login) in sold for login in rows):
                    raise ValueError("В TXT есть выданный товар. Сверьте остаток перед передачей.")
                section_name = lot["section"]
                section = self.cardinal.AD_CFG[section_name]
                if "$product" not in section.get("response", ""):
                    raise ValueError("Добавьте $product в шаблон штатной автовыдачи.")
                if not lot.get("handoff"):
                    # Persist the handoff before restoring native flags so a restart can finish it.
                    self.update(ref, handoff=True)
                settings = lot.get("native_settings", {"disable": "0"})
                for key in NATIVE_FLAGS:
                    if key in settings:
                        if settings[key] is None:
                            self.cardinal.AD_CFG.remove_option(section_name, key)
                        else:
                            section[key] = settings[key]
                self.save_native_config(section_name)
                data = dict(self.data)
                del data[ref]
                atomic_text(self.path, json.dumps(data, ensure_ascii=False))
                self.data = data
                self.workers.pop(ref, None)
                self.status.pop(ref, None)
                self.preferred.pop(ref, None)
                self.offer_checked.pop(ref, None)
                self.orders_checked.pop(ref, None)

    def update_with_notice(self, ref, key, message, **changes):
        with self.lock:
            notices = dict(self.data[ref].get("notices", {}))
            notices.setdefault(key, {"message": message, "attempts": 0, "next_attempt": 0})
            self.update(ref, notices=notices, **changes)

    def _send_notice(self, lot, message):
        tg = self.cardinal.telegram
        if tg is None or lot["chat"] not in tg.authorized_users:
            return False
        return tg.bot.send_message(lot["chat"], html.escape(message), parse_mode="HTML", timeout=10) is not None

    def notify(self, lot, message):
        try:
            self._send_notice(lot, message)
        except Exception:
            LOG.warning("TDM: Telegram notification failed for listing %s", lot["id"])

    def flush_notices(self, ref):
        if not self.notice_flush_lock.acquire(blocking=False):
            return
        try:
            with self.lock:
                lot = self.data[ref]
                now = time.time()
                due = next(((key, notice) for key, notice in lot.get("notices", {}).items()
                            if notice["next_attempt"] <= now), None)
            if due is None:
                return
            key, notice = due
            confirmation = (ref, key)
            if confirmation not in self.confirmed_notices:
                try:
                    if self._send_notice(lot, notice["message"]):
                        self.confirmed_notices.add(confirmation)
                except Exception:
                    LOG.warning("TDM: Telegram notification will be retried for listing %s", lot["id"])
            with self.lock:
                notices = dict(self.data[ref].get("notices", {}))
                if confirmation in self.confirmed_notices:
                    notices.pop(key, None)
                    # Keep the acknowledgement in memory if persisting it fails.
                    self.update(ref, notices=notices)
                    self.confirmed_notices.discard(confirmation)
                else:
                    attempts = min(notice["attempts"] + 1, 7)
                    delay = min(5 * 2 ** (attempts - 1), 300)
                    notices[key] = dict(notice, attempts=attempts, next_attempt=time.time() + delay)
                    self.update(ref, notices=notices)
        finally:
            self.notice_flush_lock.release()

    def check(self, ref, lot, login, line, foreground=False):
        try:
            with nullcontext() if foreground else self.background_requests:
                return managed_request(ref, "inspect", login, line if lot["format"] == "full" else None)["state"]
        except (OSError, ValueError, KeyError, TypeError):
            return "inventory_unknown"

    def quantity_result(self, ref, message):
        with self.lock:
            if self.data[ref].get("quantity_status") != message:
                self.update(ref, quantity_status=message)

    def paid_quantity_not_reserved(self, lot, fields):
        category_id = getattr(getattr(fields, "subcategory", None), "id", None)
        filters = {"section": f"lot-{category_id}"} if category_id else {}
        seen_cursors = set()
        seen_orders = set()
        cursor = None
        outstanding = 0
        while True:
            cursor, orders = self.cardinal.account.get_sells(start_from=cursor, state="paid",
                                                             include_closed=False, include_refunded=False, **filters)
            for order in orders:
                order_id = order.id
                if order_id in seen_orders:
                    continue
                seen_orders.add(order_id)
                if order_state(order) != "PAID":
                    continue
                order_category = getattr(getattr(order, "subcategory", None), "id", None)
                order_lot = str(getattr(order, "lot_id", ""))
                if ((category_id and order_category and category_id != order_category)
                        or (re.fullmatch(r"[1-9][0-9]*", order_lot) and order_lot != str(lot["id"]))):
                    continue
                stored = lot["orders"].get(order_id)
                if stored and (stored["sent"] or stored["logins"]):
                    continue
                amount = getattr(order, "amount", None)
                if not isinstance(amount, int) or amount < 1:
                    return None
                # The list sometimes has no offer ID. Counting all such paid
                # orders in this category keeps us from adding units twice.
                outstanding += amount
            if not cursor:
                return outstanding
            if cursor in seen_cursors:
                return None
            seen_cursors.add(cursor)

    def sync_verified_stock(self, ref, snapshot, cache, bundle):
        try:
            with self.funpay_lock, self.lock:
                lot = self.data[ref]
                if lot["retired"]:
                    return
                if bundle != managed_request(ref):
                    self.quantity_result(ref, "Состав изменился. Количество обновится после следующей сверки.")
                    return
                path, locked = self.product_file(lot)
                with locked:
                    original = product_rows(path, lot["format"])
                    blocked = self.blocked_logins(lot)
                    sold = set(lot["sold"])
                    actual = {login: line for login, line in original.items()
                              if login not in blocked and sold_key(login) not in sold}
                    if actual != snapshot:
                        self.quantity_result(ref, "TXT изменился. Количество обновится после следующей сверки.")
                        return
                    states = [cache.get(login) for login in actual]
                    if any(result is None or result[0] != sold_key(line)
                           for line, result in zip(actual.values(), states)):
                        self.quantity_result(ref, "Ожидается полный обход текущего остатка.")
                        return
                    ready = sum(result[1] == "ready" for result in states)
                    uncertain = any(result[1] not in ("ready", "missing") for result in states)
                fields = self.offer(lot)
                if not fields.active:
                    self.quantity_result(ref, "Лот скрыт. Количество будет сверено после ручной публикации.")
                    return
                if fields.amount is None:
                    self.quantity_result(ref, "В этой категории FunPay не указывает количество товара.")
                    return
                outstanding = self.paid_quantity_not_reserved(lot, fields)
                if outstanding is None:
                    self.quantity_result(ref, "FunPay не подтвердил количество в открытых заказах.")
                    return
                verified = max(0, ready - outstanding)
                if not verified:
                    self.quantity_result(ref, "Нет подтверждённого свободного остатка. Ожидается повторная сверка.")
                    return
                # Read the offer after reading orders to preserve the latest fields.
                fields = self.offer(lot)
                if not fields.active or fields.amount is None:
                    self.quantity_result(ref, "Количество изменилось на FunPay. Ожидается повторная сверка.")
                    return
                target = min(verified, fields.amount) if uncertain else verified
                if target < 1:
                    return
                if target != fields.amount:
                    with locked:
                        if product_rows(path, lot["format"]) != original:
                            self.quantity_result(ref, "TXT изменился. Количество обновится после следующей сверки.")
                            return
                        fields.amount = target
                        self.cardinal.account.save_lot(fields)
                    current = self.offer(lot)
                    if current.amount != target:
                        self.quantity_result(ref, "FunPay вернул другое количество. Ожидается повторная сверка.")
                        return
                message = f"На FunPay: {target} · подтверждённый свободный остаток: {verified}."
                if uncertain:
                    message += " Увеличение ждёт полной проверки."
                self.quantity_result(ref, message)
        except Exception as exc:
            LOG.warning("TDM: stock synchronization failed (%s)", type(exc).__name__)
            self.quantity_result(ref, "FunPay не подтвердил синхронизацию. Плагин повторит её после сверки.")

    def start_worker(self, ref):
        with self.lock:
            if ref not in self.data or ref in self.workers:
                return
            generation = object()
            self.workers[ref] = generation
        Thread(target=self.monitor, args=(ref, generation), daemon=True, name=f"TDM lot {ref}").start()

    def record_inventory_health(self, ref, success, now=None):
        now = now or dt.datetime.now(MSK)
        with self.lock:
            lot = self.data[ref]
            if lot["retired"]:
                return
            failed_since = lot.get("inventory_failed_since")
            outage_since = lot.get("inventory_outage_since")
            if success:
                changes = dict(inventory_checked_at=now.isoformat(), inventory_failed_since=None,
                               inventory_outage_since=None)
                if outage_since:
                    self.update_with_notice(ref, f"inventory-recovered:{outage_since}",
                                            f"✅ Лот #{lot['id']} · фоновая проверка восстановилась. "
                                            "Весь остаток удалось сверить с Twitch.", **changes)
                else:
                    self.update(ref, **changes)
                return
            if failed_since is None:
                self.update(ref, inventory_failed_since=now.isoformat())
                return
            if not outage_since and now - dt.datetime.fromisoformat(failed_since) >= dt.timedelta(seconds=SCAN_INTERVAL):
                self.update_with_notice(ref, f"inventory-outage:{failed_since}",
                                        f"⚠️ Лот #{lot['id']} · фоновая сверка не завершалась успешно "
                                        "уже 15 минут. Проверьте доступ к Twitch и FunPay, TXT и настройки лота.",
                                        inventory_outage_since=failed_since)

    def monitor(self, ref, generation):
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
            delay = 1
            with self.lot_operation(ref):
                if self.workers.get(ref) is not generation:
                    return
                lot = self.data[ref]
                if lot.get("handoff"):
                    try:
                        self.detach(ref)
                    except (OSError, ValueError, KeyError):
                        delay = 30
                    else:
                        return
                else:
                    try:
                        self.flush_notices(ref)
                        self.finish_sales(ref, lot)
                        lot = self.refresh_offer(ref)
                        self.recover_orders(ref, lot)
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
                            if completed and rows:
                                checked = {"ready", "missing", "claimed_unverified", "claim_unverified"}
                                self.record_inventory_health(ref, all(state in checked for state in states))
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
                                if not self.data[ref]["retired"]:
                                    self.sync_verified_stock(ref, rows, cache, bundle)
                        last_error = None
                    except Exception as exc:
                        reason = type(exc).__name__
                        if last_error != reason:
                            LOG.warning("TDM: listing %s monitoring failed (%s)", lot["id"], reason)
                            if lot["retired"]:
                                self.notify(lot, f"⚠️ Лот #{lot['id']} · проверка приостановлена. Проверьте настройки лота, TXT и связь с сервисами.")
                            last_error = reason
                        if not lot["retired"]:
                            try:
                                self.record_inventory_health(ref, False)
                            except OSError:
                                LOG.warning("TDM: could not persist inventory health for listing %s", lot["id"])
                        delay = 31
            time.sleep(delay)

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
                    self.update_with_notice(ref, "retired",
                                            f"⏸ Лот #{lot['id']} снят · {lot['retired']}.",
                                            inactive_since=now.isoformat())
                    lot = self.data[ref]
            last[ref] = time.monotonic()
            return lot

    @staticmethod
    def blocked_logins(lot):
        return {login for order in lot["orders"].values() for login in order["logins"]} | set(lot["pending"])

    def open_orders(self, lot, offer=None):
        cursor = None
        seen = set()
        category = getattr(offer, "subcategory", None)
        category_id = getattr(category, "id", None)
        filters = {"section": f"lot-{category_id}"} if category_id else {}
        while True:
            cursor, orders = self.cardinal.account.get_sells(start_from=cursor, state="paid",
                                                             include_closed=False, include_refunded=False, **filters)
            for order in orders:
                order_category = getattr(order, "subcategory", None)
                order_category_id = getattr(order_category, "id", None)
                if category_id and order_category_id and order_category_id != category_id:
                    continue
                order_lot = str(getattr(order, "lot_id", ""))
                identified = re.fullmatch(r"[1-9][0-9]*", order_lot)
                if identified and order_lot != str(lot["id"]):
                    continue
                order_id = getattr(order, "id", "")
                reference = f" #{order_id}" if re.fullmatch(r"[A-Za-z0-9]+", str(order_id)) else ""
                if not identified:
                    return f"FunPay не подтвердил лот оплаченного заказа{reference} в этой категории."
                return f"У лота открыт оплаченный заказ{reference}."
            if not cursor:
                return ""
            if cursor in seen:
                raise RuntimeError("FunPay returned an incomplete order list")
            seen.add(cursor)

    def retirement_wait(self, ref, reason):
        lot = self.data[ref]
        if reason == lot.get("retirement_waiting", ""):
            return
        if reason:
            self.update_with_notice(ref, "retirement-wait:" + sold_key(reason),
                                    f"⚠️ Лот #{lot['id']} · возврат остатка отложен. {reason}",
                                    retirement_waiting=reason)
        else:
            self.update(ref, retirement_waiting="")

    def recover_retirement_detachment(self, ref, path, stage):
        lot = self.data[ref]
        transaction = lot.get("retirement_detachment")
        if transaction is None:
            return

        def matches_stage(file):
            stat = file.stat()
            identity = (stat.st_dev, stat.st_ino)
            return identity == tuple(transaction["identity"]) and hashlib.sha256(file.read_bytes()).hexdigest() == transaction["after"]

        stat = path.stat()
        identity = (stat.st_dev, stat.st_ino)
        if not matches_stage(path) and not stage.is_symlink() and stage.is_file() and matches_stage(stage) and (
                identity == tuple(transaction["before_identity"]) and
                hashlib.sha256(path.read_bytes()).hexdigest() == transaction["before"]):
            stage.replace(path)
            if os.name != "nt":
                directory = os.open(path.parent, os.O_RDONLY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)

        pending = dict(lot["pending"])
        confirmed = set(lot.get("retirement_confirmed", ()))
        review = set(lot.get("retirement_review", ()))
        if matches_stage(path):
            for login in transaction["logins"]:
                pending[login] = True
                confirmed.add(login)
        else:
            review.update(transaction["logins"])
        self.update(ref, pending=pending, retirement_detachment=None,
                    retirement_confirmed=sorted(confirmed), retirement_review=sorted(review))
        stage.unlink(missing_ok=True)

    def retire_stock(self, ref, lot):
        inactive = lot["inactive_since"]
        if not inactive or dt.datetime.now(MSK) - dt.datetime.fromisoformat(inactive) < dt.timedelta(minutes=2):
            return
        with self.funpay_lock, self.lock:
            lot = self.data[ref]
            offer = self.offer(lot)
            reason = "FunPay ещё показывает лот активным." if offer.active else self.open_orders(lot, offer)
            if reason:
                self.retirement_wait(ref, reason)
                return
            path, locked = self.product_file(lot)
            stage = path.with_name(f".tdm-detach-{sold_key(ref)}.tmp")
            with locked:
                self.recover_retirement_detachment(ref, path, stage)
                lot = self.data[ref]
                rows = product_rows(path, lot["format"])
                order_logins = {login for item in lot["orders"].values() for login in item["logins"]}
                confirmed = set(lot.get("retirement_confirmed", ()))
                review = set(lot.get("retirement_review", ()))
                review.update(login for login in lot["pending"] if login not in rows and login not in confirmed)
                review.update(login for login in confirmed if login in rows)
                if review != set(lot.get("retirement_review", ())):
                    self.update(ref, retirement_review=sorted(review))
                sold = set(lot["sold"])
                candidates = {login: line for login, line in rows.items() if login not in order_logins | confirmed | review
                              and sold_key(login) not in sold}
        # Register accounts first. Do not hold the stock lock across a Twitch read.
        transferable = {}
        for login, line in candidates.items():
            if self.check(ref, lot, login, line) in {
                    "ready", "missing", "claimed_unverified", "claim_unverified"}:
                transferable[login] = line
        if transferable:
            with self.funpay_lock, self.lock, locked:
                lot = self.data[ref]
                offer = self.offer(lot)
                reason = "FunPay ещё показывает лот активным." if offer.active else self.open_orders(lot, offer)
                if reason:
                    self.retirement_wait(ref, reason)
                    return
                before = path.stat()
                original = path.read_bytes()
                current = product_rows(path, lot["format"], original.decode("utf-8-sig"))
                changed = {login for login, line in transferable.items() if current.get(login) != line}
                if changed:
                    review.update(changed)
                    self.update(ref, retirement_review=sorted(review))
                matched = set(transferable) - changed
                if matched:
                    # The staged inode proves that Cardinal performed the rename.
                    # The hashes also cover concurrent edits to either file.
                    next_stock = "\n".join(line for login, line in current.items() if login not in matched)
                    atomic_text(stage, next_stock)
                    staged = stage.stat()
                    self.update(ref, retirement_detachment={
                        "logins": [login for login in current if login in matched],
                        "before": hashlib.sha256(original).hexdigest(),
                        "before_identity": [before.st_dev, before.st_ino],
                        "after": hashlib.sha256(stage.read_bytes()).hexdigest(),
                        "identity": [staged.st_dev, staged.st_ino],
                    })
                    self.recover_retirement_detachment(ref, path, stage)
        with self.lock:
            lot = self.data[ref]
            review = set(lot.get("retirement_review", ()))
            releasable = [login for login in lot["pending"] if login in lot.get("retirement_confirmed", ())
                          and login not in review]
            self.retirement_wait(ref, f"Строк изменено вне журнала Cardinal: {len(review)}. Нужна ручная сверка." if review else "")
        returned = []
        reinserted = set()
        for login in releasable:
            with locked:
                if login in product_rows(path, lot["format"]):
                    reinserted.add(login)
                    continue
                try:
                    with self.background_requests:
                        status = managed_request(ref, "release", login)["state"]
                except (OSError, ValueError, KeyError, TypeError):
                    continue
                if status == "returned":
                    returned.append(login)
        if reinserted:
            with self.lock:
                current = self.data[ref]
                review = set(current.get("retirement_review", ())) | reinserted
                self.update(ref, retirement_review=sorted(review))
                self.retirement_wait(ref, f"Строк изменено вне журнала Cardinal: {len(review)}. Нужна ручная сверка.")
        if returned:
            with self.lock:
                current = self.data[ref]
                pending = {key: value for key, value in current["pending"].items() if key not in returned}
                confirmed = set(current.get("retirement_confirmed", ())) - set(returned)
                total = current.get("returned", 0) + len(returned)
                self.update_with_notice(ref, f"returned:{total}",
                                        f"♻️ Лот #{lot['id']} · в свободный пул возвращено {len(returned)}. Ожидают проверки: {len(pending)}.",
                                        pending=pending, retirement_confirmed=sorted(confirmed), returned=total)

    def guarded_delivery(self, event, ref, lot):
        from handlers import deliver_product_handler

        if not ref:
            deliver_product_handler(self.cardinal, event)
            return
        section = self.cardinal.AD_CFG[lot["section"]]
        if not self.cardinal.autodelivery_enabled:
            return
        if self.cardinal.bl_delivery_enabled and event.order.buyer_username in self.cardinal.blacklist:
            return
        order_id = event.order.id
        if order_id == "ADTEST":
            raise ValueError("Для этого лота используйте проверку в /tdm_lots. Тестовый заказ не расходует TXT.")
        if getattr(event, "lot_id", None) != lot["id"]:
            raise ValueError("Cardinal не подтвердил ID лота для этого заказа.")
        if order_state(event.order) != "PAID":
            return
        multi = self.cardinal.multidelivery_enabled and not section.getboolean("disableMultiDelivery")
        amount = event.order.amount or 1
        if not 1 <= amount <= 20 or (amount > 1 and not multi):
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
        text = entities[0]
        with self.funpay_lock:
            lot = self.refresh_offer(ref)
            chat = self.cardinal.account.get_chat_by_name(event.order.buyer_username)
            chat_id = chat.id if chat else event.order.chat_id
            history = self.cardinal.account.get_chat_history(chat_id, interlocutor_username=event.order.buyer_username)
            self.require_paid_order(event.order)
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
                rest = {login: line for login, line in rows_now.items() if login not in selected}
                stage = self.delivery_stage_path(path, ref, order_id)
                atomic_text(stage, stock_text(rest))
                order = {"sent": False, "logins": list(selected), "rows": selected, "stage": "reserved",
                         "before": stock_digest(rows_now), "after": stock_digest(rest),
                         "before_identity": stock_identity(path), "after_identity": stock_identity(stage),
                         "bundle_hash": bundle_digest(bundle),
                         "text": text, "chat": chat_id, "buyer": event.order.buyer_username,
                         "buyer_id": event.order.buyer_id, "amount": event.order.amount or 1,
                         "last_message": max((message.id for message in history), default=0)}
                self.save_order(ref, order_id, order)
                self.detach_order_stock(ref, lot, path, order_id, order, rows_now)
            self.send_reserved(ref, order_id)
        event.delivered = True
        event.delivery_text = text
        event.goods_delivered = amount
        event.goods_left = len(rows_now) - amount

    def require_paid_order(self, expected):
        fresh = self.cardinal.account.get_order(expected.id)
        if (fresh.id != expected.id or order_state(fresh) != "PAID" or fresh.buyer_id != expected.buyer_id
                or fresh.buyer_username != expected.buyer_username or (fresh.amount or 1) != (expected.amount or 1)):
            raise ValueError("FunPay не подтвердил, что заказ всё ещё оплачен. Выдача остановлена.")

    def remember_order_attention(self, ref, order_id, error):
        if order_id == "ADTEST" or not isinstance(order_id, str) or not re.fullmatch(r"[A-Za-z0-9-]{1,24}", order_id):
            return
        clues = (
            ("достаточно аккаунтов", "stock"), ("Лот снят", "retired"), ("Количество в заказе", "quantity"),
            ("Шаблон", "template"), ("$product", "template"), ("ID лота", "identity"),
            ("всё ещё оплачен", "payment"), ("TXT изменился", "stock_changed"),
            ("Состав лота изменился", "stock_changed"), ("Выдача не подтверждена", "chat"),
            ("не подтвердила ранее зарезервированный", "check"),
        )
        detail = str(error) if isinstance(error, ValueError) else ""
        reason = next((reason for clue, reason in clues if clue in detail), "unavailable")
        with self.lock:
            lot = self.data[ref]
            orders = lot["orders"]
            if orders.get(order_id, {}).get("sent"):
                return
            records = {key: value for key, value in lot.get("order_attention", {}).items()
                       if not orders.get(key, {}).get("sent")}
            current = records.get(order_id, {})
            if current.get("reason") != reason:
                records[order_id] = dict(current, reason=reason)
                try:
                    self.update(ref, order_attention=records)
                except OSError:
                    LOG.error("TDM: could not store attention reason for order %s", order_id)

    @staticmethod
    def attention_reason(order, record):
        if record.get("reason") == "stock_changed":
            return "Изменился TXT или состав лота."
        if order.get("stage") == "attempted":
            return "Ждём подтверждение отправки в чате."
        if record.get("reason") == "check":
            return "Свежая проверка не подтвердила награды на зарезервированном аккаунте."
        if order.get("stage") in ("reserved", "prepared"):
            return "Товар зарезервирован. Выдача ещё не подтверждена."
        if order.get("logins"):
            return "Старый резерв. Нужна ручная сверка чата."
        return {
            "stock": "Не хватило проверенных аккаунтов для выдачи.",
            "retired": "Лот снят с продажи.",
            "quantity": "Количество требует ручной выдачи.",
            "template": "Проверьте шаблон автовыдачи.",
            "identity": "FunPay не подтвердил ID лота.",
            "payment": "FunPay не подтвердил оплату.",
            "chat": "Ждём подтверждение отправки в чате.",
        }.get(record.get("reason"), "Проверка или отправка прервана. Нужна сверка с FunPay.")

    def attention_orders(self, ref=None):
        with self.lock:
            entries = []
            for lot_ref, lot in self.data.items():
                if ref is not None and lot_ref != ref:
                    continue
                journal = lot["orders"]
                records = lot.get("order_attention", {})
                for order_id in dict.fromkeys((*journal, *records)):
                    order = journal.get(order_id, {})
                    record = records.get(order_id, {})
                    if order.get("sent") or order.get("stage") == "refunded" or not (order.get("logins") or record):
                        continue
                    if not re.fullmatch(r"[A-Za-z0-9-]{1,24}", order_id):
                        continue
                    entries.append((lot_ref, lot["id"], order_id, self.attention_reason(order, record),
                                    len(order.get("logins", ())), record.get("payment")))
            return entries

    def reconcile_attention_order(self, ref, order_id):
        with self.funpay_lock:
            with self.lock:
                lot = self.data.get(ref)
                if lot is None:
                    raise ValueError("Лот больше не подключён.")
                order = lot["orders"].get(order_id, {})
                record = lot.get("order_attention", {}).get(order_id, {})
                if order.get("sent"):
                    return "Выдача уже подтверждена."
                if order.get("stage") == "refunded" or not (order.get("logins") or record):
                    return "Заказ больше не требует сверки."
            fresh = self.cardinal.account.get_order(order_id)
            if str(fresh.id) != order_id or ("buyer_id" in order and fresh.buyer_id != order["buyer_id"]):
                raise ValueError("FunPay не подтвердил данные заказа.")
            payment = order_state(fresh)
            if payment not in ("PAID", "CLOSED", "REFUNDED", "PARTIALLY_REFUNDED", "UNPAID"):
                payment = None
            summary = payment_status_text(payment)
            if order.get("stage") == "attempted":
                self.recover_reserved(ref, lot, order_id, order)
                if self.data[ref]["orders"][order_id]["sent"]:
                    return f"{summary}\n✅ Отправка подтверждена по чату FunPay."
            with self.lock:
                records = dict(self.data[ref].get("order_attention", {}))
                if not order.get("logins") and payment in ("CLOSED", "REFUNDED"):
                    records.pop(order_id, None)
                    self.update(ref, order_attention=records)
                    return f"{summary}\nРезерва по этому заказу нет."
                records[order_id] = dict(record, payment=payment)
                self.update(ref, order_attention=records)
            reason = self.attention_reason(order, record)
            return f"{summary}\n{reason}\nАккаунтов в резерве · {len(order.get('logins', ()))}"

    @staticmethod
    def delivery_stage_path(path, ref, order_id):
        return path.with_name(".tdm-order-" + sold_key(ref + ":" + order_id) + ".tmp")

    def detach_order_stock(self, ref, lot, path, order_id, order, rows):
        stage = self.delivery_stage_path(path, ref, order_id)
        identity = stock_identity(path)
        fingerprint = stock_digest(rows)
        if fingerprint == order["before"] and identity == order["before_identity"]:
            if (stage.is_symlink() or not stage.is_file() or stock_identity(stage) != order["after_identity"]
                    or stock_digest(product_rows(stage, lot["format"])) != order["after"]):
                raise ValueError("Не удалось подтвердить подготовленный TXT заказа.")
            if stock_identity(path) != identity or stock_digest(product_rows(path, lot["format"])) != fingerprint:
                raise ValueError("TXT изменился до подтверждения резерва.")
            stage.replace(path)
            if os.name != "nt":
                descriptor = os.open(path.parent, os.O_RDONLY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
        elif fingerprint != order["after"] or identity != order["after_identity"]:
            raise ValueError("TXT изменился до подтверждения резерва.")
        self.save_order(ref, order_id, dict(order, stage="prepared"))

    def save_order(self, ref, order_id, order):
        with self.lock:
            orders = dict(self.data[ref]["orders"], **{order_id: order})
            self.update(ref, orders=orders)

    def confirm_delivery(self, ref, order_id, order, message_id):
        with self.lock:
            lot = self.data[ref]
            current = lot["orders"][order_id]
            if current["sent"]:
                return
            orders = dict(lot["orders"], **{order_id: {
                "sent": True, "logins": order["logins"], "message_id": message_id}})
            count = len(order["logins"])
            self.update_with_notice(ref, f"delivery:{order_id}",
                                    f"✅ Лот #{lot['id']} · заказ #{order_id} · отправлено аккаунтов: {count}.",
                                    orders=orders, sold=lot["sold"] + [sold_key(login) for login in order["logins"]])

    def send_reserved(self, ref, order_id):
        from types import SimpleNamespace

        current = self.data[ref]["orders"][order_id]
        if current.get("stage") != "prepared":
            return
        if bundle_digest(managed_request(ref)) != current["bundle_hash"]:
            raise ValueError("Состав лота изменился после резервирования заказа.")
        self.require_paid_order(SimpleNamespace(id=order_id, buyer_id=current["buyer_id"],
                                                buyer_username=current["buyer"], amount=current["amount"]))
        # Once a send begins, resolve ambiguities against chat history. Repeating it
        # after a transport error can disclose the same credentials a second time.
        attempted = dict(current, stage="attempted", text_hash=delivery_text_hash(current["text"]))
        attempted.pop("rows")
        attempted.pop("text")
        self.save_order(ref, order_id, attempted)
        result = self.cardinal.send_message(current["chat"], current["text"], current["buyer"],
                                            attempts=1, watermark=False)
        if result and len(result) == 1 and self.matches_delivery_message(attempted, result[0]):
            self.confirm_delivery(ref, order_id, attempted, result[0].id)
            return
        raise ValueError("Выдача не подтверждена. Плагин сверит чат FunPay, резерв пока сохранён.")

    def matches_delivery_message(self, order, message):
        number = getattr(message, "id", 0)
        return (isinstance(number, int) and number > order["last_message"]
                and getattr(message, "author_id", None) == self.cardinal.account.id
                and delivery_text_hash(getattr(message, "text", None)) == order["text_hash"])

    def recover_reserved(self, ref, lot, order_id, order):
        stage = order.get("stage")
        if stage == "attempted":
            history = self.cardinal.account.get_chat_history(order["chat"], interlocutor_username=order["buyer"],
                                                             from_id=order["last_message"] + 1)
            match = next((message for message in history if self.matches_delivery_message(order, message)), None)
            if match is not None:
                self.confirm_delivery(ref, order_id, order, match.id)
            return
        if stage not in ("reserved", "prepared"):
            return  # Older reservations contain no trustworthy send evidence.
        fresh = self.cardinal.account.get_order(order_id)
        if (fresh.id != order_id or fresh.buyer_id != order["buyer_id"]
                or fresh.buyer_username != order["buyer"] or (fresh.amount or 1) != order["amount"]):
            raise ValueError("Данные заказа в FunPay изменились.")
        if order_state(fresh) == "PAID" and not lot["retired"]:
            for login, line in order["rows"].items():
                if self.check(ref, lot, login, line, foreground=True) != "ready":
                    raise ValueError("Свежая проверка не подтвердила ранее зарезервированный аккаунт.")
        path, locked = self.product_file(lot)
        with self.lock, locked:
            rows = product_rows(path, lot["format"])
            fingerprint = stock_digest(rows)
            if order_state(fresh) == "REFUNDED":
                reserved = order["rows"]
                identity = stock_identity(path)
                if stage == "reserved" and (fingerprint, identity) not in (
                        (order["before"], order["before_identity"]), (order["after"], order["after_identity"])):
                    raise ValueError("TXT изменился до подтверждения резерва.")
                if any(rows.get(login, line) != line for login, line in reserved.items()):
                    raise ValueError("Данные зарезервированного аккаунта в TXT изменились.")
                atomic_text(path, stock_text(dict(rows, **reserved)))
                self.save_order(ref, order_id, {"sent": False, "logins": [], "stage": "refunded"})
                self.delivery_stage_path(path, ref, order_id).unlink(missing_ok=True)
                return
            if order_state(fresh) != "PAID" or lot["retired"]:
                return
            if stage == "reserved":
                self.detach_order_stock(ref, lot, path, order_id, order, rows)
            elif set(rows).intersection(order["logins"]):
                raise ValueError("Ранее зарезервированный аккаунт снова появился в TXT.")
        self.send_reserved(ref, order_id)

    def recover_orders(self, ref, lot):
        now = time.monotonic()
        if now - self.orders_checked.get(ref, -120) < 60:
            return
        self.orders_checked[ref] = now
        if not self.cardinal.autodelivery_enabled:
            return
        with self.funpay_lock:
            lot = self.data[ref]
            for order_id, order in lot["orders"].items():
                try:
                    if not order["sent"] and order["logins"]:
                        self.recover_reserved(ref, lot, order_id, order)
                except Exception as exc:
                    self.remember_order_attention(ref, order_id, exc)
                    LOG.warning("TDM: order %s awaits reconciliation (%s)", order_id, type(exc).__name__)
        if lot["retired"] or not lot.get("delivery_since"):
            return
        self.recover_paid_orders(ref, lot)

    def recover_paid_orders(self, ref, lot):
        from FunPayAPI.updater.events import NewOrderEvent
        from handlers import setup_event_attributes_handler

        since = dt.datetime.fromisoformat(lot["delivery_since"])
        cursor = None
        seen = set()
        while True:
            with self.funpay_lock:
                cursor, orders = self.cardinal.account.get_sells(start_from=cursor, state="paid",
                                                                 include_closed=False, include_refunded=False)
            for order in orders:
                if (order.id in self.data[ref]["orders"] or order_state(order) != "PAID"
                        or not order.date or order.date.astimezone(dt.timezone.utc) < since):
                    continue
                event = NewOrderEvent("tdm-recovery", order)
                setup_event_attributes_handler(self.cardinal, event)
                if event.lot_id == lot["id"]:
                    try:
                        self.guarded_delivery(event, ref, lot)
                    except Exception as exc:
                        self.remember_order_attention(ref, order.id, exc)
                        LOG.warning("TDM: recovered order %s held (%s)", order.id, type(exc).__name__)
                        continue
                    return
            if not cursor:
                return
            if cursor in seen:
                raise RuntimeError("FunPay repeated an order-list cursor")
            seen.add(cursor)

    def install(self):
        import handlers
        with self.funpay_lock, self.lock:
            for ref, lot in tuple(self.data.items()):
                try:
                    if not lot.get("handoff"):
                        self.protect_native_stock(lot)
                    if not lot.get("delivery_since"):
                        self.update(ref, delivery_since=dt.datetime.now(dt.timezone.utc).isoformat())
                except (OSError, ValueError, KeyError) as exc:
                    LOG.error("TDM: native stock protection not persisted for listing %s (%s)", lot["id"], type(exc).__name__)
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
            with self.lot_operation(ref):
                current = self.data.get(ref)
                if current is None:
                    # An event waiting for the handoff may predate the last paid-order check.
                    if event.order.id in lot["orders"]:
                        return
                    if event.order.id == "ADTEST" or order_state(self.cardinal.account.get_order(event.order.id)) == "PAID":
                        return original(cardinal, event)
                    return
                lot = current
                try:
                    if len(matches) > 1:
                        raise ValueError("Заказ совпал с несколькими управляемыми лотами.")
                    if lot.get("handoff"):
                        raise ValueError("Лот передаётся штатной автовыдаче. Проверьте заказ в Cardinal.")
                    if not self.cardinal.plugins[UUID].enabled:
                        raise ValueError("Автоматизация лотов отключена. Заказ требует внимания продавца.")
                    with self.funpay_lock, self.lock:
                        self.protect_native_stock(lot)
                    self.guarded_delivery(event, ref, lot)
                except Exception as exc:
                    event.error = 1
                    event.error_text = "Автовыдача приостановлена. Проверьте заказ в Cardinal."
                    self.remember_order_attention(ref, event.order.id, exc)
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
                    orders = dict(current["orders"], **{order_id: dict(current["orders"][order_id], logins=[])})
                    self.update(ref, orders=orders)


def init_telegram(crd, *_):
    tg = crd.telegram
    from secrets import token_urlsafe
    from Utils import cardinal_tools
    automation = LotAutomation(crd, cardinal_tools)
    crd.tdm_lot_automation = automation
    if tg is None:
        return
    from telebot.apihelper import ApiTelegramException
    from telebot.types import InlineKeyboardButton, InlineKeyboardMarkup

    bot = tg.bot
    running = Lock()
    diagnosing = Lock()
    detach_lock = Lock()
    detach_confirmations = {}
    callback_card = local()
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
        card_chat, message_id = getattr(callback_card, "target", (None, None))
        if keyboard is not None and card_chat == chat and message_id is not None:
            try:
                bot.edit_message_text(formatted, chat_id=chat, message_id=message_id,
                                      parse_mode="HTML", reply_markup=keyboard)
                return
            except ApiTelegramException as exc:
                if exc.error_code != 400:
                    raise
                description = exc.description.casefold()
                if "message is not modified" in description:
                    return
                if not any(reason in description for reason in (
                    "message to edit not found", "message can't be edited", "message_id_invalid",
                    "message identifier is not specified", "there is no text in the message to edit",
                )):
                    raise
        bot.send_message(chat, formatted, parse_mode="HTML", reply_markup=keyboard)

    def lot_status(title, lot):
        if lot is None:
            if not crd.autodelivery_enabled or crd.AD_CFG[title].getboolean("disable", fallback=False):
                return "Выдача выключена"
            return "Штатная выдача"
        if lot.get("handoff"):
            return "Отвязка"
        if lot["retired"]:
            return "Снят с продажи" if lot["inactive_since"] else "Ожидает снятия"
        return "Подключён"

    def lot_back(ref):
        keyboard = InlineKeyboardMarkup()
        keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
        return keyboard

    def show_lots(chat, page):
        entries = sorted(lots().items(), key=lambda entry: entry[1][0].casefold())
        page = max(0, min(page, max(0, len(entries) - 1) // 8))
        keyboard = InlineKeyboardMarkup()
        for ref, (title, _) in entries[page * 8:(page + 1) * 8]:
            status = lot_status(title, automation.data.get(ref))
            keyboard.add(InlineKeyboardButton(f"{status} · {title[:43]}", callback_data="tdm-lots:lot:" + ref))
        if page:
            keyboard.add(InlineKeyboardButton("Назад", callback_data=f"tdm-lots:page:{page-1}"))
        if (page + 1) * 8 < len(entries):
            keyboard.add(InlineKeyboardButton("Дальше", callback_data=f"tdm-lots:page:{page+1}"))
        unresolved = len(automation.attention_orders())
        if unresolved:
            keyboard.add(InlineKeyboardButton(f"⚠️ Заказы требуют внимания · {unresolved}", callback_data="tdm-lots:attention:all/0"))
        keyboard.add(InlineKeyboardButton("🔌 Диагностика связи", callback_data="tdm-lots:diagnose:link"))
        text = "🏷 Лоты Twitch Drops\n\nВыберите запись автовыдачи Cardinal." if entries else "Нет записей автовыдачи с доступным TXT."
        reply(chat, text, keyboard)

    def show_diagnostics(chat, ref=None, path=None, product_lock=None):
        if not diagnosing.acquire(blocking=False):
            reply(chat, "⏳ Диагностика уже выполняется.")
            return

        def run():
            try:
                sample = None
                sample_unreadable = False
                if path is not None:
                    try:
                        with automation.lock, product_lock:
                            lot = automation.data.get(ref)
                            blocked = automation.blocked_logins(lot) if lot else set()
                            sold = set(lot["sold"]) if lot else set()
                            sample = lot_stock_sample(path, blocked, sold, limit=1)
                    except (OSError, ValueError):
                        sample_unreadable = True
                result = orchestrator_diagnostics(ref, sample, sample_unreadable)
                keyboard = InlineKeyboardMarkup()
                keyboard.add(InlineKeyboardButton("🔄 Повторить проверку", callback_data=f"tdm-lots:diagnose:{ref or 'link'}"))
                label, action = ("⬅️ К лоту", f"tdm-lots:lot:{ref}") if ref else ("🏷 К лотам", "tdm-lots:page:0")
                keyboard.add(InlineKeyboardButton(label, callback_data=action))
                reply(chat, f"🔌 Диагностика связи\n\nПлагин Cardinal · {VERSION}\n\n{result}", keyboard)
            finally:
                diagnosing.release()

        try:
            reply(chat, "⏳ Проверяю связь. Результат появится в этом чате.")
            Thread(target=run, daemon=True).start()
        except Exception:
            diagnosing.release()
            raise

    def show_attention(chat, selection, page):
        ref = None if selection == "all" else selection
        entries = automation.attention_orders(ref)
        page = max(0, min(page, max(0, len(entries) - 1) // 8))
        keyboard = InlineKeyboardMarkup()
        for lot_ref, lot_id, order_id, _, _, _ in entries[page * 8:(page + 1) * 8]:
            keyboard.add(InlineKeyboardButton(f"📦 #{order_id} · лот #{lot_id}",
                                              callback_data=f"tdm-lots:order:{lot_ref}/{order_id}"))
        if page:
            keyboard.add(InlineKeyboardButton("⬅️ Назад", callback_data=f"tdm-lots:attention:{selection}/{page-1}"))
        if (page + 1) * 8 < len(entries):
            keyboard.add(InlineKeyboardButton("Дальше ➡️", callback_data=f"tdm-lots:attention:{selection}/{page+1}"))
        if ref is not None and ref in lots():
            keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
        keyboard.add(InlineKeyboardButton("🏷 К лотам", callback_data="tdm-lots:page:0"))
        detail = (f"Ожидают сверки · {len(entries)}\nОткройте заказ, чтобы посмотреть причину и резерв."
                  if entries else "Все заказы обработаны, открытых сверок нет.")
        reply(chat, "⚠️ Заказы требуют внимания\n\n" + detail, keyboard)

    def show_attention_order(chat, action, ref, order_id):
        keyboard = InlineKeyboardMarkup()
        record = next((entry for entry in automation.attention_orders(ref) if entry[2] == order_id), None)
        result = None
        if record is not None:
            if action == "reco":
                try:
                    result = automation.reconcile_attention_order(ref, order_id)
                except Exception as exc:
                    LOG.warning("TDM: manual order %s reconciliation failed (%s)", order_id, type(exc).__name__)
                    result = "⚠️ FunPay не подтвердил сверку. Попробуйте позже."
            if any(entry[2] == order_id for entry in automation.attention_orders(ref)):
                keyboard.add(InlineKeyboardButton("🔄 Сверить с FunPay", callback_data=f"tdm-lots:reco:{ref}/{order_id}"))
        keyboard.add(InlineKeyboardButton("⬅️ К заказам лота", callback_data=f"tdm-lots:attention:{ref}/0"))
        keyboard.add(InlineKeyboardButton("📦 Все заказы", callback_data="tdm-lots:attention:all/0"))
        if record is None:
            reply(chat, "Заказ больше не требует сверки.", keyboard)
            return
        _, lot_id, _, reason, reserved, payment = record
        detail = result or f"{payment_status_text(payment)}\n{reason}\nАккаунтов в резерве · {reserved}"
        reply(chat, f"📦 Заказ #{order_id} · лот #{lot_id}\n\n{detail}", keyboard)

    def open_menu(message):
        if authorized(message, message.from_user):
            show_lots(message.chat.id, 0)

    def enable_lot(chat, ref, item, offer_id, product_format):
        title, path = item
        keyboard = lot_back(ref)
        try:
            bundle = automation.enable(ref, title, path.name, offer_id, product_format, chat)
        except ValueError as exc:
            reply(chat, f"⚠️ {exc}", keyboard)
        except error.HTTPError as exc:
            reply(chat, f"⚠️ Оркестратор ответил HTTP {exc.code}. Проверьте связь и состав лота.", keyboard)
        except Exception as exc:
            LOG.warning("TDM: listing %s could not be enabled (%s)", offer_id, type(exc).__name__)
            reply(chat, "⚠️ Проверьте ID лота, доступность TXT и связь с оркестратором.", keyboard)
        else:
            reply(chat, f"✅ Лот #{offer_id} подключён.\n{bundle['Game']} · наград в наборе: {len(bundle['Rewards'])}.\n"
                        "Проверка остатка и автовыдачи работают в фоне.", keyboard)

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

    def preview_detach(chat, ref):
        with automation.lock:
            lot = automation.data.get(ref)
            if lot is not None:
                generation = (lot["id"], lot.get("delivery_since"))
        if lot is None:
            reply(chat, "Лот уже отвязан. Откройте /tdm_lots заново.")
            return
        token = token_urlsafe(12)
        with detach_lock:
            detach_confirmations[chat] = (token, time.monotonic() + 180, ref, generation)
        keyboard = InlineKeyboardMarkup()
        keyboard.add(InlineKeyboardButton("🔌 Подтвердить отвязку", callback_data=f"tdm-lots:detach-ok:{token}"))
        keyboard.add(InlineKeyboardButton("⬅️ Отмена", callback_data=f"tdm-lots:detach-cancel:{token}"))
        reply(chat, f"🔌 Отвязка лота #{lot['id']}\n\n"
                    "Временно скройте лот на FunPay и завершите открытые заказы. "
                    "При подтверждении плагин сверит заказы и резервы, затем включит штатную выдачу Cardinal "
                    "по текущему остатку TXT.", keyboard)

    def accept_detach(chat, token, cancel):
        with detach_lock:
            pending = detach_confirmations.get(chat)
            if pending is not None and pending[0] == token:
                del detach_confirmations[chat]
            else:
                pending = None
        if pending is None or time.monotonic() >= pending[1]:
            reply(chat, "Подтверждение устарело. Откройте карточку лота в /tdm_lots и повторите отвязку.")
            return
        _, _, ref, generation = pending
        keyboard = InlineKeyboardMarkup()
        keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
        if cancel:
            reply(chat, "Отвязка отменена.", keyboard)
            return

        def run():
            try:
                automation.detach(ref, expected_generation=generation)
            except ValueError as exc:
                reply(chat, f"⚠️ {exc}", keyboard)
            except Exception as exc:
                LOG.warning("TDM: listing %s could not be detached (%s)", generation[0], type(exc).__name__)
                reply(chat, "⚠️ Cardinal не завершил отвязку. Проверьте связь с FunPay и повторите действие.", keyboard)
            else:
                text = f"✅ Лот #{generation[0]} отвязан. Для остатка TXT восстановлена штатная выдача Cardinal."
                if not crd.autodelivery_enabled:
                    text += "\nВключите глобальную автовыдачу Cardinal, чтобы обрабатывать новые заказы."
                reply(chat, text, keyboard)

        reply(chat, "⏳ Проверяю заказы и резервы.")
        Thread(target=run, daemon=True).start()

    def handle(call):
        bot.answer_callback_query(call.id)
        message = call.message
        if message is None or not authorized(message, call.from_user):
            return
        _, action, value = call.data.split(":", 2)
        chat = message.chat.id
        callback_card.target = (chat, getattr(message, "message_id", None))
        try:
            navigate(chat, action, value)
        finally:
            del callback_card.target

    def navigate(chat, action, value):
        if action == "page" and value.isdecimal():
            show_lots(chat, int(value))
            return
        if action == "diagnose" and value == "link":
            show_diagnostics(chat)
            return
        if action == "detach" and re.fullmatch(r"[a-f0-9]{24}", value):
            preview_detach(chat, value)
            return
        if action in ("detach-ok", "detach-cancel"):
            accept_detach(chat, value, cancel=action == "detach-cancel")
            return
        ref, _, product_format = value.partition("/")
        if action == "attention" and (ref == "all" or ref in automation.data) and product_format.isdecimal():
            show_attention(chat, ref, int(product_format))
            return
        if action in ("order", "reco") and ref in automation.data and re.fullmatch(r"[A-Za-z0-9-]{1,24}", product_format):
            show_attention_order(chat, action, ref, product_format)
            return
        item = lots().get(ref)
        if item is None:
            reply(chat, "Запись автовыдачи изменилась. Открой /tdm_lots заново.")
            return
        title, path = item
        product_lock = cardinal_tools.get_products_file_lock(f"storage/products/{path.name}")
        lot = automation.data.get(ref)
        if action == "diagnose" and value == ref:
            show_diagnostics(chat, ref, path, product_lock)
            return
        if action == "manage":
            keyboard = InlineKeyboardMarkup()
            keyboard.add(InlineKeyboardButton("🔌 Проверить доступ к Twitch", callback_data=f"tdm-lots:diagnose:{ref}"))
            keyboard.add(lot_code_button(InlineKeyboardButton, ref))
            text = f"⚙️ Управление лотом\n\n{title[:150]}"
            if lot is not None:
                keyboard.add(InlineKeyboardButton("🔌 Отвязать от оркестратора", callback_data=f"tdm-lots:detach:{ref}"))
                text += "\n\nПеред отвязкой скройте лот на FunPay и завершите открытые заказы."
            keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
            reply(chat, text, keyboard, lot_code=ref)
            return
        if action == "check":
            if lot is None:
                keyboard = InlineKeyboardMarkup()
                keyboard.add(InlineKeyboardButton("Логин:пароль", callback_data=f"tdm-lots:run:{ref}/login-password"),
                             InlineKeyboardButton("Полный TXT", callback_data=f"tdm-lots:run:{ref}/full"))
                keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
                reply(chat, "🔎 Проверка до 10 аккаунтов\n\nВыберите формат товарного TXT.", keyboard)
                return
            action, product_format = "run", lot["format"]
        if action == "setup" and lot is not None:
            action = "lot"
        if action == "lot":
            count = cardinal_tools.count_products(f"storage/products/{path.name}")
            keyboard = InlineKeyboardMarkup()
            text = f"{title[:150]}\n\nСтатус · {lot_status(title, lot)}\nОстаток в Cardinal · {count}\nФайл · {path.name}"
            if lot is None:
                keyboard.add(InlineKeyboardButton("🔗 Подключить лот", callback_data=f"tdm-lots:setup:{ref}"))
            else:
                text += f"\nЛот FunPay · #{lot['id']}"
                if lot["deadline"]:
                    cutoff = dt.datetime.fromisoformat(lot["deadline"]["at"])
                    text += f"\n📅 Снятие по дате · {cutoff:%d.%m.%Y %H:%M} МСК"
                if lot["retired"]:
                    text += f"\n⏸ {lot['retired'].capitalize()}." + (" Снят с продажи." if lot["inactive_since"] else " Ожидается подтверждение FunPay.")
                    text += f"\n♻️ Возвращено · {lot.get('returned', 0)} · ожидают проверки · {len(lot['pending'])}"
                    if lot.get("retirement_waiting"):
                        text += "\n⏳ " + lot["retirement_waiting"]
                elif ref in automation.status:
                    total, ready, missing, unknown = automation.status[ref]
                    text += f"\n📊 Состав подтверждён · {ready}/{total}\nНаграды отсутствуют · {missing}\nПодтверждение недоступно · {unknown}"
                    text += f"\nОжидают первого замера · {total - ready - missing - unknown}"
                else:
                    text += "\nОжидание первого замера."
                if lot.get("inventory_checked_at"):
                    checked = dt.datetime.fromisoformat(lot["inventory_checked_at"]).astimezone(MSK)
                    text += f"\n🕒 Последняя полная сверка · {checked:%d.%m.%Y %H:%M} МСК"
                elif not lot["retired"]:
                    text += "\n🕒 Полная сверка ещё не завершалась."
                if lot.get("inventory_outage_since") and not lot["retired"]:
                    outage = dt.datetime.fromisoformat(lot["inventory_outage_since"]).astimezone(MSK)
                    text += f"\n⚠️ Сверка недоступна с {outage:%d.%m.%Y %H:%M} МСК"
                if lot.get("quantity_status") and not lot["retired"]:
                    text += "\n🏷 FunPay · " + lot["quantity_status"]
                unresolved = len(automation.attention_orders(ref))
                if unresolved:
                    keyboard.add(InlineKeyboardButton(f"⚠️ Заказы требуют внимания · {unresolved}",
                                                      callback_data=f"tdm-lots:attention:{ref}/0"))
                keyboard.add(InlineKeyboardButton("📋 Состав и сроки", callback_data=f"tdm-lots:bundle:{ref}"))
            keyboard.add(InlineKeyboardButton("🔎 Проверить до 10 аккаунтов", callback_data=f"tdm-lots:check:{ref}"))
            keyboard.add(InlineKeyboardButton("🔄 Обновить", callback_data=f"tdm-lots:lot:{ref}"),
                         InlineKeyboardButton("⚙️ Управление", callback_data=f"tdm-lots:manage:{ref}"))
            keyboard.add(InlineKeyboardButton("⬅️ К лотам", callback_data="tdm-lots:page:0"))
            reply(chat, text, keyboard)
            return
        if action in ("setup", "edit"):
            keyboard = InlineKeyboardMarkup()
            try:
                with automation.lock, product_lock:
                    blocked = automation.blocked_logins(lot) if lot else set()
                    sold = set(lot["sold"]) if lot else set()
                    sample = lot_stock_sample(path, blocked, sold)
                registration = managed_request(ref, "register", title=title, sample=sample)
                if registration.get("state") != "registered":
                    raise ValueError("Registration was not acknowledged")
                try:
                    bundle = managed_request(ref) if action == "setup" else None
                except error.HTTPError as exc:
                    if exc.code != 404:
                        raise
                    bundle = None
            except (OSError, ValueError, TypeError, AttributeError):
                keyboard.add(InlineKeyboardButton("🔌 Проверить связь", callback_data="tdm-lots:diagnose:link"))
                keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
                reply(chat, "Не удалось открыть настройку состава. Проверьте связь с оркестратором и доступность TXT.", keyboard)
                return
            if not bundle or not bundle.get("Rewards"):
                username = registration.get("telegram_username", "")
                if isinstance(username, str) and re.fullmatch(r"[A-Za-z0-9_]{5,32}", username):
                    keyboard.add(InlineKeyboardButton("📋 Открыть состав в оркестраторе", url=f"https://t.me/{username}?start=lot_{ref}"))
                    instructions = "Откройте оркестратор и сохраните состав наград."
                else:
                    instructions = "Откройте в оркестраторе «Лоты → Составы лотов», выберите этот лот и сохраните состав наград."
                if action == "setup":
                    keyboard.add(InlineKeyboardButton("➡️ Продолжить подключение", callback_data=f"tdm-lots:setup:{ref}"))
                    instructions += " Затем вернитесь сюда и продолжите подключение."
                keyboard.add(InlineKeyboardButton("⬅️ К лоту", callback_data=f"tdm-lots:lot:{ref}"))
                reply(chat, f"📋 Состав лота\n\n{title[:150]}\n\n{instructions}", keyboard)
                return

            profile = crd.tg_profile or crd.profile
            candidates = [offer for offer in profile.get_sorted_lots(1).values()
                          if same_lot_title(title, offer.description)] if profile is not None else []
            if len(candidates) != 1:
                reply(chat, "✅ Состав сохранён.\n\nНе удалось определить ID предложения FunPay. "
                            "Возьмите ID из ссылки offer?id=… и подставьте его в команду. "
                            "Для полного TXT замените login-password на full.\n\n"
                            f"/tdm_lot_enable {ref} ID login-password", lot_back(ref))
                return
            offer_id = candidates[0].id
            for short, label in (("lp", "Логин:пароль"), ("txt", "Полный TXT")):
                keyboard.add(InlineKeyboardButton(f"Подключить · {label}", callback_data=f"tdm-lots:enable:{ref}/{offer_id}/{short}"))
            keyboard.add(InlineKeyboardButton("⬅️ Отмена", callback_data=f"tdm-lots:lot:{ref}"))
            reply(chat, f"🔗 Подключение лота #{offer_id}\n\n✅ Состав сохранён · {len(bundle['Rewards'])} наград.\n"
                        "Выберите формат товарного TXT, чтобы включить автоматизацию.", keyboard)
            return
        if action == "bundle":
            keyboard = InlineKeyboardMarkup()
            keyboard.add(InlineKeyboardButton("📋 Изменить состав", callback_data=f"tdm-lots:edit:{ref}"))
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
            keyboard = lot_back(ref)
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
                                f"Данных для вывода недостаточно · {len(states) - ready - missing}", keyboard)
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
                reply(chat, text, keyboard)
            except error.HTTPError as exc:
                reply(chat, f"Оркестратор ответил HTTP {exc.code}. Проверьте связь и сохранённый состав лота.", keyboard)
            except (OSError, ValueError, KeyError, TypeError):
                reply(chat, "Проверка не запустилась. Проверьте связь с оркестратором и формат TXT.", keyboard)
            finally:
                running.release()

        try:
            reply(chat, "Проверка запущена. Результат появится в этом чате.")
            Thread(target=run, daemon=True).start()
        except Exception:
            running.release()
            raise

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
