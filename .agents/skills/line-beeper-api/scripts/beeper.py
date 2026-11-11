#!/usr/bin/env python3
"""Drive and read Beeper Desktop's local API for beeper/line feature tests.

Every command that reads messages or sends works only on chats in the
allowlist (see `allow`). `discover` lists LINE chat names so the user can
pick them; it never reads messages.

Token: BEEPER_ACCESS_TOKEN, else the keychain item `beeper-desktop-api`
(macOS `security`, Linux `secret-tool`). Base URL: BEEPER_API_URL, default
http://localhost:23373. Allowlist: BEEPER_LINE_ALLOWLIST, default
$XDG_CONFIG_HOME/beeper-line/allowlist.json (~/.config/...).
Python 3.8+, standard library only.
"""

import argparse
import hashlib
import json
import mimetypes
import os
import platform
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

BASE_URL = os.environ.get("BEEPER_API_URL", "http://localhost:23373").rstrip("/")
KEYCHAIN_SERVICE = "beeper-desktop-api"
ATTACHMENT_TYPES = ["image", "video", "audio", "file", "gif", "voice-note", "sticker"]
# Message fields kept in snapshots and compared by diff.
MESSAGE_FIELDS = ["type", "text", "isSender", "isDeleted", "sendStatus"]
ATTACHMENT_FIELDS = ["type", "mimeType", "fileName", "fileSize", "isGif", "isSticker",
                     "isVoiceNote", "duration", "size"]


def die(msg, code=1):
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(code)


# ---------------------------------------------------------------- token / http

def load_token():
    token = os.environ.get("BEEPER_ACCESS_TOKEN", "").strip()
    if token:
        return token
    system = platform.system()
    if system == "Darwin":
        cmd = ["security", "find-generic-password", "-s", KEYCHAIN_SERVICE, "-w"]
    elif system == "Linux":
        cmd = ["secret-tool", "lookup", "service", KEYCHAIN_SERVICE]
    else:
        cmd = None
    if cmd:
        try:
            out = subprocess.run(cmd, capture_output=True, text=True, check=False)
            if out.returncode == 0 and out.stdout.strip():
                return out.stdout.strip()
        except FileNotFoundError:
            pass
    die("no Beeper API token. Create one in Beeper Desktop (Settings > Integrations > "
        "'+' next to Approved connections) and save it as the keychain item "
        f"'{KEYCHAIN_SERVICE}', or set BEEPER_ACCESS_TOKEN.")


_TOKEN = None


def api(method, path, body=None, query=None, data=None, content_type=None):
    global _TOKEN
    if _TOKEN is None:
        _TOKEN = load_token()
    url = BASE_URL + path
    if query:
        url += "?" + urllib.parse.urlencode({k: v for k, v in query.items() if v is not None})
    headers = {"Authorization": f"Bearer {_TOKEN}", "Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        content_type = "application/json"
    if content_type:
        headers["Content-Type"] = content_type
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as e:
        detail = e.read().decode(errors="replace")[:500]
        die(f"{method} {path} -> HTTP {e.code}: {detail}")
    except urllib.error.URLError as e:
        die(f"cannot reach Beeper Desktop at {BASE_URL} ({e.reason}). Is it running "
            "with the API enabled (Settings > Integrations)?")
    return json.loads(raw) if raw.strip() else None


def q(chat_id):
    return urllib.parse.quote(chat_id, safe="")


# ------------------------------------------------------------------ allowlist

def allowlist_path():
    env = os.environ.get("BEEPER_LINE_ALLOWLIST")
    if env:
        return Path(env).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
    return Path(base) / "beeper-line" / "allowlist.json"


def load_allowlist():
    path = allowlist_path()
    if not path.exists():
        return {"chats": []}
    return json.loads(path.read_text())


def save_allowlist(data):
    path = allowlist_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
    try:
        path.chmod(0o600)
    except OSError:
        pass


def match_allowed(chats, ref):
    """Allowlist entries that `ref` names: an exact chat ID wins, else every
    entry whose local chat ID or label equals it. Dies when that is more than one."""
    by_id = [c for c in chats if c["id"] == ref]
    if by_id:
        return by_id
    found = [c for c in chats if ref in (c.get("localChatID"), c.get("label"))]
    if len(found) > 1:
        die(f"'{ref}' names {len(found)} allowed chats: "
            + ", ".join(f"{c.get('label')} ({c['id']})" for c in found)
            + ". Use the chat ID.", code=3)
    return found


def resolve_allowed(ref):
    """Return the allowlist entry for a chat ID, local chat ID or exact label."""
    chats = load_allowlist()["chats"]
    if not chats:
        die(f"the allowlist is empty ({allowlist_path()}). Run `discover`, ask the user "
            "which test chats to allow, then `allow add <chatID>`.", code=3)
    found = match_allowed(chats, ref)
    if found:
        return found[0]
    die(f"chat '{ref}' is not in the allowlist. Allowed: "
        + ", ".join(f"{c.get('label')} ({c.get('localChatID') or c['id']})" for c in chats)
        + ". Ask the user before adding it with `allow add`.", code=3)


# --------------------------------------------------------------- LINE helpers

def is_line_account(acc):
    bridge = acc.get("bridge") or {}
    ids = [str(acc.get("accountID", "")), str(bridge.get("id", ""))]
    return any(i == "line" or i.startswith(("line_", "sh-line", "line-")) for i in ids) \
        or str(acc.get("network", "")).lower() == "line"


def line_account_ids(explicit=None):
    if explicit:
        return [explicit]
    accounts = api("GET", "/v1/accounts")
    if isinstance(accounts, dict):
        accounts = accounts.get("items", [])
    ids = [a["accountID"] for a in accounts if is_line_account(a)]
    if not ids:
        die("no LINE account found in Beeper Desktop. Pass --account <accountID>.")
    return ids


def local_file_info(src_url):
    """sha256, size and (for GIFs) frame count of Beeper's cached copy."""
    if not src_url or not src_url.startswith("file://"):
        return None
    path = Path(urllib.parse.unquote(urllib.parse.urlparse(src_url).path))
    if not path.is_file():
        return None
    blob = path.read_bytes()
    info = {"localBytes": len(blob), "sha256": hashlib.sha256(blob).hexdigest()}
    if blob[:6] in (b"GIF87a", b"GIF89a"):
        info["gifFrames"] = gif_frame_count(blob)
    return info


def gif_frame_count(b):
    try:
        i = 13
        if b[10] & 0x80:
            i += 3 * (2 ** ((b[10] & 0x07) + 1))
        frames = 0
        while i < len(b):
            block = b[i]
            if block == 0x3B:
                break
            if block == 0x21:
                i += 2
            elif block == 0x2C:
                frames += 1
                flags = b[i + 9]
                i += 10
                if flags & 0x80:
                    i += 3 * (2 ** ((flags & 0x07) + 1))
                i += 1
            else:
                return None
            while i < len(b) and b[i]:
                i += b[i] + 1
            i += 1
        return frames
    except IndexError:
        return None


def summarize(msg, with_file=True):
    out = {"id": msg.get("id"), "timestamp": msg.get("timestamp"),
           "sender": msg.get("senderName")}
    for f in MESSAGE_FIELDS:
        if f in msg and msg[f] is not None:
            out[f] = msg[f]
    atts = []
    for a in msg.get("attachments") or []:
        item = {f: a[f] for f in ATTACHMENT_FIELDS if f in a and a[f] is not None}
        if with_file:
            item.update(local_file_info(a.get("srcURL")) or {})
        atts.append(item)
    if atts:
        out["attachments"] = atts
    return out


def one_line(s):
    parts = [s.get("id", "?"), (s.get("timestamp") or "")[:19], s.get("sender") or "", s.get("type", "")]
    for a in s.get("attachments", []):
        size = a.get("size") or {}
        parts.append(f"[{a.get('mimeType') or '?'} {a.get('fileName') or ''} "
                     f"{a.get('fileSize', '?')}B {size.get('width', '?')}x{size.get('height', '?')}"
                     f"{' gif' if a.get('isGif') else ''}{' sticker' if a.get('isSticker') else ''}]")
    if s.get("text"):
        parts.append(json.dumps(s["text"][:80], ensure_ascii=False))
    status = (s.get("sendStatus") or {}).get("status")
    if status and status != "SUCCESS":
        parts.append(f"sendStatus={status}")
    return "  ".join(str(p) for p in parts)


def fetch_messages(chat_id, limit):
    items, cursor = [], None
    while len(items) < limit:
        page = api("GET", f"/v1/chats/{q(chat_id)}/messages",
                   query={"cursor": cursor, "direction": "before" if cursor else None})
        batch = page.get("items", [])
        items.extend(batch)
        cursor = page.get("oldestCursor")
        if not page.get("hasMore") or not batch or not cursor:
            break
    return items[:limit]


def wait_for_message(chat_id, pending_id, timeout):
    deadline = time.time() + timeout
    msg = None
    while time.time() < deadline:
        msg = api("GET", f"/v1/chats/{q(chat_id)}/messages/{urllib.parse.quote(pending_id, safe='')}")
        status = ((msg or {}).get("sendStatus") or {}).get("status")
        if msg and status in (None, "SUCCESS", "FAIL_PERMANENT", "FAIL_RETRIABLE"):
            return msg
        time.sleep(1.5)
    return msg


# ------------------------------------------------------------------- commands

def cmd_discover(args):
    accounts = line_account_ids(args.account)
    page = api("GET", "/v1/chats", query={"accountIDs": ",".join(accounts), "limit": args.limit})
    allowed = {c["id"] for c in load_allowlist()["chats"]}
    for c in page.get("items", []):
        if c.get("accountID") not in accounts:
            continue
        if args.query and args.query.lower() not in (c.get("title") or "").lower():
            continue
        mark = "*" if c["id"] in allowed else " "
        print(f"{mark} {c.get('localChatID', ''):>8}  {c.get('type', ''):<6}  "
              f"{(c.get('lastActivity') or '')[:10]}  {c.get('title', '')}  ({c['id']})")
    print("\n* = already allowed. Only names are listed; no messages were read.")


def cmd_allow(args):
    data = load_allowlist()
    if args.action == "list":
        print(f"# {allowlist_path()}")
        for c in data["chats"]:
            print(f"{c.get('localChatID', ''):>8}  {c.get('type', ''):<6}  {c.get('label')}  ({c['id']})")
        if not data["chats"]:
            print("(empty)")
        return
    if not args.chats:
        die("give one or more chat IDs")
    if args.action == "add":
        accounts = line_account_ids(args.account)
        for ref in args.chats:
            chat = api("GET", f"/v1/chats/{q(ref)}")
            if chat.get("accountID") not in accounts:
                die(f"{ref} is not a LINE chat ({chat.get('accountID')}); refusing to allow it")
            data["chats"] = [c for c in data["chats"] if c["id"] != chat["id"]]
            data["chats"].append({"id": chat["id"], "localChatID": chat.get("localChatID"),
                                  "label": chat.get("title"), "type": chat.get("type")})
            print(f"allowed: {chat.get('title')} ({chat['id']})")
    else:
        before = len(data["chats"])
        drop = {c["id"] for ref in args.chats for c in match_allowed(data["chats"], ref)}
        data["chats"] = [c for c in data["chats"] if c["id"] not in drop]
        print(f"removed {before - len(data['chats'])} chat(s)")
    save_allowlist(data)


def cmd_messages(args):
    chat = resolve_allowed(args.chat)
    for m in fetch_messages(chat["id"], args.limit):
        print(one_line(summarize(m, with_file=False)))


def cmd_show(args):
    chat = resolve_allowed(args.chat)
    msg = api("GET", f"/v1/chats/{q(chat['id'])}/messages/{urllib.parse.quote(args.message, safe='')}")
    out = {"chat": chat.get("label"), "message": summarize(msg)}
    if args.event:
        out["rawEvent"] = api("GET", f"/_matrix/client/v3/rooms/{q(chat['id'])}/event/"
                                     f"{urllib.parse.quote(args.event, safe='')}")
        if out["rawEvent"] is None:
            out["rawEvent"] = f"not found: {args.event} is not an event in this chat"
    if args.full:
        out["apiMessage"] = msg
    print(json.dumps(out, indent=2, ensure_ascii=False))


def report_sent(chat, sent, timeout):
    msg = wait_for_message(sent["chatID"], sent["pendingMessageID"], timeout)
    if not msg:
        print(json.dumps({"chat": chat.get("label"), "pendingMessageID": sent["pendingMessageID"],
                          "note": "not resolved yet; check later with `show`"}, indent=2))
        return
    print(json.dumps({"chat": chat.get("label"), "message": summarize(msg)}, indent=2, ensure_ascii=False))


def cmd_send_text(args):
    chat = resolve_allowed(args.chat)
    sent = api("POST", f"/v1/chats/{q(chat['id'])}/messages", body={"text": args.text})
    report_sent(chat, sent, args.timeout)


def cmd_send_file(args):
    chat = resolve_allowed(args.chat)
    path = Path(args.file)
    if not path.is_file():
        die(f"no such file: {path}")
    mime = args.mime or mimetypes.guess_type(path.name)[0]
    boundary = uuid.uuid4().hex
    parts = []
    fields = {"fileName": path.name}
    if mime:
        fields["mimeType"] = mime
    for k, v in fields.items():
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
    parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\n'
                 f'Content-Type: {mime or "application/octet-stream"}\r\n\r\n'.encode())
    parts.append(path.read_bytes())
    parts.append(f"\r\n--{boundary}--\r\n".encode())
    up = api("POST", "/v1/assets/upload", data=b"".join(parts),
             content_type=f"multipart/form-data; boundary={boundary}")
    if not up or up.get("error") or not up.get("uploadID"):
        die(f"upload failed: {up}")
    attachment = {"uploadID": up["uploadID"]}
    if args.type:
        attachment["type"] = args.type
    body = {"attachment": attachment}
    if args.caption:
        body["text"] = args.caption
    print(json.dumps({"upload": {k: up.get(k) for k in ("fileName", "mimeType", "fileSize", "width",
                                                        "height", "duration")}}, ensure_ascii=False))
    sent = api("POST", f"/v1/chats/{q(chat['id'])}/messages", body=body)
    report_sent(chat, sent, args.timeout)


def cmd_snapshot(args):
    chat = resolve_allowed(args.chat)
    if args.ids:
        msgs = [api("GET", f"/v1/chats/{q(chat['id'])}/messages/{urllib.parse.quote(i, safe='')}")
                for i in args.ids.split(",")]
    else:
        msgs = fetch_messages(chat["id"], args.limit)
    snap = {"chat": chat.get("label"), "chatID": chat["id"],
            "taken": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "label": args.label,
            "messages": [summarize(m) for m in msgs]}
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(snap, indent=2, ensure_ascii=False) + "\n")
    print(f"wrote {len(msgs)} message(s) to {args.output}")


def diff_key(m, seen):
    atts = m.get("attachments") or []
    key = atts[0].get("fileName") if atts and atts[0].get("fileName") else (m.get("text") or m.get("type"))
    seen[key] = seen.get(key, 0) + 1
    return key if seen[key] == 1 else f"{key} #{seen[key]}"


def comparable(m):
    out = {f: m.get(f) for f in ("type", "text")}
    out["sendStatus"] = (m.get("sendStatus") or {}).get("status")
    for i, a in enumerate(m.get("attachments") or []):
        for f in ATTACHMENT_FIELDS + ["localBytes", "sha256", "gifFrames"]:
            if f in a:
                out[f"att{i}.{f}"] = a[f]
    return out


def cmd_diff(args):
    a, b = (json.loads(Path(p).read_text()) for p in (args.before, args.after))
    # An after snapshot taken with --limit can still hold the before phase's
    # messages. Drop those (same message ID), so a repeated sample is compared
    # with its new send, not with itself. If every message is shared, the same
    # messages were read twice: compare them all.
    before_ids = {m.get("id") for m in a["messages"]} - {None}
    newer = [m for m in b["messages"] if m.get("id") not in before_ids]
    skipped = len(b["messages"]) - len(newer) if newer else 0
    after_msgs = newer or b["messages"]

    # Newest first, so a sample that a snapshot holds more than once (an older
    # test run) pairs its latest copy in each snapshot.
    def newest_first(msgs):
        return sorted(msgs, key=lambda m: m.get("timestamp") or "", reverse=True)

    sa, sb = {}, {}
    before = {diff_key(m, sa): comparable(m) for m in newest_first(a["messages"])}
    after = {diff_key(m, sb): comparable(m) for m in newest_first(after_msgs)}
    if skipped:
        print(f"skipped {skipped} message(s) in {args.after} that are also in {args.before}")
    changed = 0
    for key in list(before) + [k for k in after if k not in before]:
        x, y = before.get(key), after.get(key)
        if x is None or y is None:
            print(f"{'only after ' if x is None else 'only before'}: {key}")
            continue
        fields = [f for f in sorted(set(x) | set(y)) if x.get(f) != y.get(f)]
        if fields:
            changed += 1
            print(f"changed: {key}")
            for f in fields:
                print(f"    {f}: {json.dumps(x.get(f), ensure_ascii=False)} -> {json.dumps(y.get(f), ensure_ascii=False)}")
    print(f"\n{len(before)} before, {len(after)} after, {changed} changed "
          "(matched by attachment file name, else text)")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--account", help="LINE accountID in Beeper (default: auto-detect)")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("discover", help="list LINE chat names to pick for the allowlist")
    s.add_argument("--query", help="filter titles (case-insensitive substring)")
    s.add_argument("--limit", type=int, default=50)
    s.set_defaults(func=cmd_discover)

    s = sub.add_parser("allow", help="list, add or remove allowed chats")
    s.add_argument("action", choices=["list", "add", "remove"])
    s.add_argument("chats", nargs="*", help="chat ID, local chat ID or label")
    s.set_defaults(func=cmd_allow)

    s = sub.add_parser("messages", help="one line per recent message")
    s.add_argument("chat")
    s.add_argument("--limit", type=int, default=20)
    s.set_defaults(func=cmd_messages)

    s = sub.add_parser("show", help="one message with attachment details and the cached file's hash")
    s.add_argument("chat")
    s.add_argument("message", help="message ID, pendingMessageID or Matrix event ID")
    s.add_argument("--event", help="Matrix event ID ($...) to also fetch the raw event")
    s.add_argument("--full", action="store_true", help="include the API's whole message object")
    s.set_defaults(func=cmd_show)

    s = sub.add_parser("send-text", help="send a text message")
    s.add_argument("chat")
    s.add_argument("text")
    s.add_argument("--timeout", type=float, default=30)
    s.set_defaults(func=cmd_send_text)

    s = sub.add_parser("send-file", help="upload a file and send it")
    s.add_argument("chat")
    s.add_argument("file")
    s.add_argument("--type", choices=ATTACHMENT_TYPES, help="attachment type hint (default: from mimetype)")
    s.add_argument("--mime", help="override the mimetype")
    s.add_argument("--caption")
    s.add_argument("--timeout", type=float, default=60)
    s.set_defaults(func=cmd_send_file)

    s = sub.add_parser("snapshot", help="save message details to JSON for a later diff")
    s.add_argument("chat")
    s.add_argument("-o", "--output", required=True)
    s.add_argument("--limit", type=int, default=20)
    s.add_argument("--ids", help="comma-separated message IDs instead of the latest --limit")
    s.add_argument("--label", help="free text, e.g. 'before' or a build hash")
    s.set_defaults(func=cmd_snapshot)

    s = sub.add_parser("diff", help="compare two snapshots")
    s.add_argument("before")
    s.add_argument("after")
    s.set_defaults(func=cmd_diff)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
