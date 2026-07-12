#!/usr/bin/env python3
import json
import logging
import os
import re
import subprocess
import time
from pathlib import Path
from urllib.parse import urlparse
from urllib.request import Request, build_opener, ProxyHandler
from urllib.error import HTTPError

UPSTREAM_BASE = os.environ.get("UPSTREAM_OPS_BASE", "http://127.0.0.1:6818").rstrip("/")
SYNC_USERNAME_ENV = "UPSTREAM_OPS_SYNC_USERNAME"
SYNC_PASSWORD_ENV = "UPSTREAM_OPS_SYNC_PASSWORD"
CONFIG_PATH = "/opt/upstream-ops/data/config.yaml"
LOG_FILE = "/opt/upstream-ops/data/logs/sub2-pool-sync.log"
SKIP_HINTS = ["cpa", "cliproxy", "172.18.0.1", "localhost", "127.0.0.1"]
SKIP_ACCOUNT_NAME_PREFIXES = ("uo-",)
SKIP_SITES = {
    "https://api.dstopology.com",
    "https://sub.kedaya.xyz",
    "https://ark.cn-beijing.volces.com",
}
DEFAULT_EVENTS = [
    "balance_low",
    "rate_changed",
    "rate_structure_changed",
    "rate_added",
    "rate_removed",
    "monitor_failed",
    "sub2_pool_changed",
]

def configure_logging():
    Path(LOG_FILE).parent.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(filename=LOG_FILE, level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    console = logging.StreamHandler()
    console.setFormatter(logging.Formatter("%(levelname)s %(message)s"))
    logging.getLogger().addHandler(console)


def load_simple_config(path=CONFIG_PATH):
    data = {}
    section = None
    for raw in Path(path).read_text().splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        if raw and not raw.startswith(" ") and raw.endswith(":"):
            section = raw[:-1].strip()
            data.setdefault(section, {})
            continue
        if section and raw.startswith("    ") and ":" in raw:
            k, v = raw.strip().split(":", 1)
            v = v.strip().strip('"').strip("'")
            if v.lower() == "true":
                v = True
            elif v.lower() == "false":
                v = False
            else:
                try:
                    v = int(v)
                except Exception:
                    pass
            data[section][k] = v
    return data


def auth_config():
    cfg = load_simple_config().get("auth", {})
    username = str(cfg.get("username", "") or "").strip()
    password = str(cfg.get("password", "") or "")
    if not username or not password:
        raise RuntimeError("UpstreamOps auth configuration is incomplete")
    return username, password


def sync_credentials():
    username = os.environ.get(SYNC_USERNAME_ENV, "").strip()
    password = os.environ.get(SYNC_PASSWORD_ENV, "")
    if not username or not password:
        raise RuntimeError(f"{SYNC_USERNAME_ENV} and {SYNC_PASSWORD_ENV} must be configured")
    return username, password


def proxy_url():
    cfg = load_simple_config().get("proxy", {})
    if not cfg.get("enabled"):
        return ""
    host = str(cfg.get("host", "")).strip()
    port = cfg.get("port", 0)
    if not host or not port:
        return ""
    protocol = str(cfg.get("protocol", "http") or "http")
    user = str(cfg.get("username", "") or "")
    password = str(cfg.get("password", "") or "")
    auth = f"{user}:{password}@" if user or password else ""
    return f"{protocol}://{auth}{host}:{port}"


def sh(cmd, input_text=None):
    p = subprocess.run(cmd, input=input_text, text=True, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode != 0:
        raise RuntimeError(f"cmd failed: {cmd}; stderr={p.stderr[:500]}")
    return p.stdout


def http_json(method, url, payload=None, headers=None, timeout=20, use_proxy=False):
    h = {"Content-Type": "application/json"}
    if headers:
        h.update(headers)
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = Request(url, data=data, headers=h, method=method)
    opener = build_opener(ProxyHandler({"http": proxy_url(), "https": proxy_url()})) if use_proxy and proxy_url() else build_opener()
    try:
        with opener.open(req, timeout=timeout) as resp:
            text = resp.read().decode("utf-8", "replace")
            try:
                return resp.status, json.loads(text), text
            except Exception:
                return resp.status, {"raw": text[:500]}, text
    except HTTPError as e:
        text = e.read().decode("utf-8", "replace")
        try:
            return e.code, json.loads(text), text
        except Exception:
            return e.code, {"raw": text[:500]}, text
    except Exception as e:
        return 0, {"error": str(e)}, ""


def normalize_site(base):
    base = (base or "").strip()
    if not base:
        return ""
    parsed = urlparse(base if re.match(r"^https?://", base) else "https://" + base)
    scheme = parsed.scheme or "https"
    netloc = parsed.netloc
    path = parsed.path or ""
    for suffix in ["/v1", "/api/v1", "/api/plan/v3", "/api/coding/v3"]:
        if path.rstrip("/").endswith(suffix):
            path = path.rstrip("/")[: -len(suffix)]
            break
    return f"{scheme}://{netloc}{path.rstrip('/')}".rstrip("/")


def is_image_only(name):
    label = re.sub(r"\s+", " ", str(name or "")).strip().lower()
    return (
        label.startswith("生图")
        or label.startswith("图 ")
        or label.startswith("图-")
        or label.startswith("image")
        or label.startswith("gpt image")
        or label.startswith("grok image")
    )


def pool_mode_enabled(creds):
    value = creds.get("pool_mode")
    if value is True:
        return True
    return isinstance(value, str) and value.strip().lower() in ("1", "true", "yes", "on")


def fetch_sub2_candidates():
    sql = r"""
copy (
  select id, name, platform, type, schedulable, credentials::text, coalesce(notes,'')
  from accounts
  where deleted_at is null and status='active'
  order by platform, type, id
) to stdout with csv delimiter E'\t' quote E'\b';
"""
    out = sh("docker exec -i sub2api-db sh -lc 'psql -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\" -qAt'", sql)
    by_site = {}
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) < 7:
            continue
        aid, name, platform, typ, sched, creds_text, notes = parts[:7]
        hay = f"{name} {creds_text} {notes}".lower()
        if any(x in hay for x in SKIP_HINTS):
            continue
        if str(name or "").strip().lower().startswith(SKIP_ACCOUNT_NAME_PREFIXES):
            continue
        try:
            creds = json.loads(creds_text)
        except Exception:
            continue
        if not pool_mode_enabled(creds):
            continue
        site = normalize_site(creds.get("base_url", ""))
        if not site:
            continue
        key = site.lower().rstrip("/")
        if key not in by_site:
            by_site[key] = {
                "source_id": int(aid),
                "source_names": [],
                "site": site,
            }
        if is_image_only(name):
            continue
        by_site[key]["source_names"].append(name)

    candidates = []
    image_only_skipped = 0
    for item in by_site.values():
        if not item["source_names"]:
            image_only_skipped += 1
            continue
        item["source_name"] = " / ".join(item["source_names"])
        candidates.append(item)
    return candidates, image_only_skipped


def upstream_token():
    user, password = auth_config()
    status, obj, _ = http_json("POST", UPSTREAM_BASE + "/api/auth/login", {"username": user, "password": password}, timeout=10)
    token = (obj.get("data") or {}).get("token")
    if status != 200 or not token:
        raise RuntimeError(f"UpstreamOps login failed: {status} {obj}")
    return token


def list_upstream_channels(token):
    status, obj, _ = http_json("GET", UPSTREAM_BASE + "/api/channels?page=1&page_size=-1", headers={"Authorization": "Bearer " + token}, timeout=15)
    if status != 200:
        raise RuntimeError(f"list channels failed: {status} {obj}")
    return (obj.get("data") or {}).get("items") or []


def probe_type(item):
    site = item["site"].rstrip("/")
    username, password = sync_credentials()
    last = ""
    for ctype in ("newapi", "sub2api"):
        if ctype == "newapi":
            status, obj, raw = http_json("POST", site + "/api/user/login", {"username": username, "password": password}, timeout=20, use_proxy=True)
            if status == 200 and obj.get("success") is True:
                return "newapi", "ok"
        else:
            status, obj, raw = http_json("POST", site + "/api/v1/auth/login", {"email": username, "password": password}, timeout=20, use_proxy=True)
            if status == 200 and obj.get("code") == 0:
                return "sub2api", "ok"
        last = f"{ctype} status={status} {str(obj)[:160]}"
    return "", last


def unique_name(item):
    parsed = urlparse(item["site"])
    label = f"{parsed.netloc}{parsed.path}".strip("/")
    label = re.sub(r"[^A-Za-z0-9._-]+", "-", label).strip("-")
    return f"URL-{label or parsed.netloc}"[:120]


def create_channel(token, item, ctype):
    username, password = sync_credentials()
    payload = {
        "name": unique_name(item),
        "type": ctype,
        "site_url": item["site"],
        "username": username,
        "password": password,
        "credential_mode": "password",
        "sort_order": 700,
        "login_extra_params": "{}",
        "turnstile_enabled": False,
        "ignore_announcements": False,
        "subscription_enabled": ctype == "sub2api",
        "proxy_enabled": True,
        "balance_threshold": 5,
        "recharge_multiplier_mode": "divide",
        "monitor_enabled": False,
    }
    status, obj, raw = http_json("POST", UPSTREAM_BASE + "/api/channels", payload, headers={"Authorization": "Bearer " + token}, timeout=15)
    if status != 200:
        return None, f"create failed status={status} body={raw[:180]}"
    return (obj.get("data") or {}).get("id"), "created"


def set_channel_enabled(token, cid, enabled):
    action = "enable" if enabled else "disable"
    status, obj, raw = http_json(
        "POST",
        f"{UPSTREAM_BASE}/api/channels/{cid}/{action}",
        {},
        headers={"Authorization": "Bearer " + token},
        timeout=15,
    )
    if status != 200:
        return False, f"{action} failed status={status} body={raw[:180]}"
    return True, ""


def test_channel(token, cid):
    status, obj, raw = http_json("POST", f"{UPSTREAM_BASE}/api/channels/{cid}/test-login", {}, headers={"Authorization": "Bearer " + token}, timeout=45)
    compact = raw.replace("\n", " ")[:500]
    lower = compact.lower()
    bad = any(x in lower for x in ["失败", "failed", "error", "authenticationerror", "unauthorized", "invalid", "status 401", "status 403", "401", "403", "1010", "just a moment", "turnstile token 为空"])
    return status == 200 and not bad, compact


def list_notification_channels(token):
    status, obj, _ = http_json(
        "GET",
        UPSTREAM_BASE + "/api/notifications/channels",
        headers={"Authorization": "Bearer " + token},
        timeout=15,
    )
    if status != 200:
        raise RuntimeError(f"list notification channels failed: {status} {obj}")
    return obj.get("data") or []


def ensure_notification_subscription(token):
    channels = list_upstream_channels(token)
    channel_ids = [c["id"] for c in channels if c.get("monitor_enabled")]
    subs = json.dumps([{"channel_ids": channel_ids, "mode": "all", "events": DEFAULT_EVENTS}], ensure_ascii=False)
    feishu_channels = [item for item in list_notification_channels(token) if item.get("type") == "feishu"]
    if not feishu_channels:
        raise RuntimeError("no Feishu notification channel is configured")
    for channel in feishu_channels:
        payload = {
            "name": channel.get("name", ""),
            "type": channel.get("type", "feishu"),
            "subscriptions": subs,
            "enabled": bool(channel.get("enabled", True)),
            "proxy_enabled": bool(channel.get("proxy_enabled", False)),
        }
        status, obj, raw = http_json(
            "PUT",
            f"{UPSTREAM_BASE}/api/notifications/channels/{channel['id']}",
            payload,
            headers={"Authorization": "Bearer " + token},
            timeout=15,
        )
        if status != 200:
            raise RuntimeError(f"update Feishu notification subscription failed: {status} {raw[:180] or obj}")
    return len(channel_ids)


def main():
    configure_logging()
    token = upstream_token()
    existing = list_upstream_channels(token)
    existing_by_site = {}
    for channel in existing:
        site = normalize_site(channel.get("site_url") or "").lower().rstrip("/")
        if not site:
            continue
        existing_by_site.setdefault(site, []).append(channel)
    candidates, image_only_skipped = fetch_sub2_candidates()
    added = []
    enabled = []
    skipped = []
    for item in candidates:
        site_key = item["site"].lower().rstrip("/")
        existing_channels = existing_by_site.get(site_key, [])
        if any(c.get("monitor_enabled") for c in existing_channels):
            continue
        if existing_channels:
            channel = existing_channels[0]
            cid = channel.get("id")
            ok, detail = test_channel(token, cid)
            if ok:
                changed, reason = set_channel_enabled(token, cid, True)
                if changed:
                    enabled.append((cid, item["site"], channel.get("type") or "", item["source_name"][:80]))
                else:
                    skipped.append((item["site"], reason))
            else:
                skipped.append((item["site"], detail[:220]))
            continue
        if site_key in SKIP_SITES:
            skipped.append((item["site"], "known_manual_blocked"))
            continue
        ctype, reason = probe_type(item)
        if not ctype:
            skipped.append((item["site"], reason or "probe_failed"))
            continue
        cid, reason = create_channel(token, item, ctype)
        if not cid:
            skipped.append((item["site"], reason))
            continue
        time.sleep(2)
        ok, detail = test_channel(token, cid)
        if ok:
            changed, reason = set_channel_enabled(token, cid, True)
            if changed:
                added.append((cid, item["site"], ctype, item["source_name"][:80]))
                existing_by_site.setdefault(site_key, []).append({"id": cid, "monitor_enabled": True})
            else:
                skipped.append((item["site"], reason))
        else:
            skipped.append((item["site"], detail[:220]))
    subscribed = ensure_notification_subscription(token)
    logging.info(
        "sync done candidates=%d image_only_skipped=%d added=%d enabled=%d skipped=%d subscribed_channels=%d",
        len(candidates),
        image_only_skipped,
        len(added),
        len(enabled),
        len(skipped),
        subscribed,
    )
    print(json.dumps({
        "candidates": len(candidates),
        "image_only_skipped": image_only_skipped,
        "added": [{"id": x[0], "site": x[1], "type": x[2], "source": x[3]} for x in added],
        "enabled": [{"id": x[0], "site": x[1], "type": x[2], "source": x[3]} for x in enabled],
        "skipped": [{"site": x[0], "reason": x[1]} for x in skipped[:20]],
        "subscribed_channels": subscribed,
    }, ensure_ascii=False))

if __name__ == "__main__":
    main()
