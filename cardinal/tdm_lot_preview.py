"""Read-only preview of a Cardinal product file against TDM inventories."""
import argparse
import datetime as dt
import json
import logging
import os
from pathlib import Path
import re
from threading import Thread
from urllib import error, request

NAME = "TDM Drops lot preview"
VERSION = "0.1.0"
DESCRIPTION = "Checks the inventory of unsold accounts in a configured lot."
CREDITS = "TDM Light Orchestrator"
UUID = "b66cfbb9-9fe6-40e1-b9da-cf1d0e326542"
SETTINGS_PAGE = False
BIND_TO_DELETE = None

LOG = logging.getLogger(__name__)
URL = "http://127.0.0.1:8787/internal/lot-preview"
LOCAL_HTTP = request.build_opener(request.ProxyHandler({}))
LOGIN = re.compile(r"[a-zA-Z0-9_]+")


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
        with LOCAL_HTTP.open(http_request, timeout=13) as response:
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
    settings = json.loads(config_path.read_text(encoding="utf-8-sig"))
    token = os.environ.get("ORCHESTRATOR_LOT_PREVIEW_TOKEN", "")
    if len(token) < 32:
        raise ValueError("ORCHESTRATOR_LOT_PREVIEW_TOKEN must be set")
    product_format = settings["format"]
    if product_format not in ("full", "login-password"):
        raise ValueError("Choose full or login-password product format")
    path = Path(settings["products_path"])
    modified = path.stat().st_mtime_ns
    logins = read_logins(path, product_format)
    limit = int(settings.get("sample_limit", 10))
    if not 1 <= limit <= 100:
        raise ValueError("sample_limit must be from 1 to 100")
    campaigns = settings["campaign_ids"]
    if not isinstance(campaigns, list) or not 1 <= len(campaigns) <= 20 or not all(isinstance(i, str) and i for i in campaigns):
        raise ValueError("Specify 1 to 20 campaign IDs")
    now = dt.datetime.now(dt.timezone.utc)
    cutoff = suggested_cutoff(settings, campaigns, now)
    # Include only row numbers in the report. Send the lookup login over loopback.
    rows = [dict(row=index, **inspect_inventory(settings["game"], campaigns, login, token))
            for index, login in logins[:limit]]
    return {
        "lot_id": str(settings["lot_id"]),
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


def on_start(*_):
    config = Path("configs/tdm-lot-preview.json")
    if not config.is_file():
        return

    def run():
        try:
            result = write_preview(config)
            LOG.info("TDM lot preview complete: %d accounts checked", result["sampled"])
        except (OSError, ValueError, KeyError, TypeError):
            LOG.warning("TDM lot preview failed. Verify the product path, format and credentials token.")

    Thread(target=run, daemon=True).start()


BIND_TO_POST_START = [on_start]


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=DESCRIPTION)
    cli.add_argument("config", type=Path)
    write_preview(cli.parse_args().config)
