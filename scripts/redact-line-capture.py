#!/usr/bin/env python3
"""Redact sensitive values from LINE capture JSON files.

Usage:
    python3 scripts/redact-line-capture.py input.json [output.json]

The default output path is input-redacted.json. This helper preserves JSON
structure for protocol analysis while removing credentials, cookies, tokens,
session identifiers, email addresses, and obvious long secrets.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
from typing import Any


SENSITIVE_KEYS = {
    "authorization",
    "cookie",
    "set-cookie",
    "x-hmac",
    "x-line-access",
    "x-line-channeltoken",
    "x-line-next-access-token",
    "x-line-refresh-token",
    "x-line-session-id",
    "x-obs-params",
    "password",
    "passwd",
    "email",
    "mail",
    "token",
    "access_token",
    "refresh_token",
    "session",
    "certificate",
}

SENSITIVE_KEY_FRAGMENTS = (
    "auth",
    "cookie",
    "credential",
    "password",
    "secret",
    "session",
    "token",
)

EMAIL_RE = re.compile(r"(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}")
JWT_RE = re.compile(r"eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}")
MID_RE = re.compile(r"\b([ucr][0-9a-f]{20,})\b", re.IGNORECASE)
LONG_SECRET_RE = re.compile(r"^[A-Za-z0-9+/=_-]{96,}$")


def marker(kind: str, value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8", errors="ignore")).hexdigest()[:12]
    return f"<redacted:{kind}:len={len(value)}:sha256={digest}>"


def key_is_sensitive(key: str) -> bool:
    normalized = key.lower()
    if normalized in SENSITIVE_KEYS:
        return True
    return any(fragment in normalized for fragment in SENSITIVE_KEY_FRAGMENTS)


def redact_string(value: str, force: str | None = None) -> str:
    if not value:
        return value
    if force:
        return marker(force, value)

    value = JWT_RE.sub(lambda match: marker("jwt", match.group(0)), value)
    value = EMAIL_RE.sub(lambda match: marker("email", match.group(0)), value)
    value = MID_RE.sub(lambda match: match.group(1)[0].lower() + marker("mid", match.group(1)), value)

    stripped = value.strip()
    if LONG_SECRET_RE.match(stripped):
        return marker("opaque", stripped)
    return value


def redact(value: Any, parent_key: str = "") -> Any:
    if isinstance(value, dict):
        redacted = {}
        for key, child in value.items():
            key_text = str(key)
            if key_is_sensitive(key_text):
                if isinstance(child, (dict, list)):
                    redacted[key] = marker(key_text.lower(), json.dumps(child, sort_keys=True, default=str))
                else:
                    redacted[key] = redact_string(str(child), key_text.lower())
            else:
                redacted[key] = redact(child, key_text)
        return redacted

    if isinstance(value, list):
        return [redact(item, parent_key) for item in value]

    if isinstance(value, str):
        return redact_string(value)

    return value


def default_output_path(input_path: str) -> str:
    base, ext = os.path.splitext(input_path)
    return f"{base}-redacted{ext or '.json'}"


def main() -> int:
    if len(sys.argv) not in (2, 3):
        print(__doc__, file=sys.stderr)
        return 1

    input_path = sys.argv[1]
    output_path = sys.argv[2] if len(sys.argv) == 3 else default_output_path(input_path)

    with open(input_path, "r", encoding="utf-8") as infile:
        data = json.load(infile)

    redacted = redact(data)

    with open(output_path, "w", encoding="utf-8") as outfile:
        json.dump(redacted, outfile, indent=2, ensure_ascii=False)
        outfile.write("\n")

    try:
        os.chmod(output_path, 0o600)
    except OSError:
        pass

    print(f"Redacted capture written to: {output_path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
