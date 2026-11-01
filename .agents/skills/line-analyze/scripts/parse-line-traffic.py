#!/usr/bin/env python3
"""Parse LINE network captures (CDP or net-log format).

Usage:
    python3 .agents/skills/line-analyze/scripts/parse-line-traffic.py <capture.json> [output.json]

Supports two input formats:
- CDP capture (from line-cdp-capture.py): has SSE message bodies, Thrift response bodies
- Chrome net-log: lower-level, lacks streaming body data

Extracts HTTP requests/responses to LINE domains and writes a structured
JSON summary suitable for analysis by Claude or other tools.
"""

import json
import sys
import os
from collections import defaultdict

LINE_DOMAINS = [
    "line-chrome-gw.line-apps.com",
    "obs.line-apps.com",
    "legy-jp.line-apps.com",
    "gd2.line.naver.jp",
    "gwx.line.naver.jp",
    "gw.line.naver.jp",
    "api.line.me",
    "stickershop.line-scdn.net",
    "profile.line-scdn.net",
    "obs.line-scdn.net",
    "scdn.line-apps.com",
]


def is_line_domain(url: str) -> bool:
    """Check if URL belongs to a LINE domain."""
    for domain in LINE_DOMAINS:
        if domain in url:
            return True
    return False


def parse_netlog(filepath: str) -> dict:
    """Parse Chrome net-log JSON and extract LINE traffic."""
    print(f"Reading {filepath}...", file=sys.stderr)
    file_size = os.path.getsize(filepath)
    print(f"File size: {file_size / 1024 / 1024:.1f} MB", file=sys.stderr)

    with open(filepath, "r") as f:
        data = json.load(f)

    constants = data.get("constants", {})
    events = data.get("events", [])

    # Build reverse maps from numeric IDs to names
    event_types = {}
    if "logEventTypes" in constants:
        for name, num in constants["logEventTypes"].items():
            event_types[num] = name

    source_types = {}
    if "logSourceType" in constants:
        for name, num in constants["logSourceType"].items():
            source_types[num] = name

    phases = {}
    if "logEventPhase" in constants:
        for name, num in constants["logEventPhase"].items():
            phases[num] = name

    # Group events by source ID
    sources = defaultdict(list)
    for event in events:
        sid = event.get("source", {}).get("id")
        if sid is not None:
            sources[sid].append(event)

    # Extract URL_REQUEST sources that hit LINE domains
    line_requests = []
    for sid, evts in sources.items():
        source_type = evts[0].get("source", {}).get("type")
        source_name = source_types.get(source_type, str(source_type))

        if source_name != "URL_REQUEST":
            continue

        # Find the URL
        url = None
        method = None
        status_code = None
        request_headers = {}
        response_headers = {}
        request_body = None
        response_body_parts = []

        for evt in evts:
            etype = event_types.get(evt.get("type"), "")
            params = evt.get("params", {})

            if etype == "URL_REQUEST_START_JOB" or etype == "REQUEST_ALIVE":
                if "url" in params:
                    url = params["url"]
                if "method" in params:
                    method = params["method"]

            elif etype == "HTTP_TRANSACTION_SEND_REQUEST_HEADERS":
                if "headers" in params:
                    for h in params["headers"]:
                        if ": " in h:
                            k, v = h.split(": ", 1)
                            request_headers[k] = v
                        elif h.startswith("GET ") or h.startswith("POST ") or h.startswith("PUT "):
                            parts = h.split(" ")
                            method = parts[0]
                if "line" in params:
                    line_parts = params["line"].split(" ")
                    if len(line_parts) >= 1:
                        method = line_parts[0]

            elif etype == "HTTP_TRANSACTION_READ_RESPONSE_HEADERS":
                if "headers" in params:
                    for h in params["headers"]:
                        if h.startswith("HTTP/"):
                            parts = h.split(" ")
                            if len(parts) >= 2:
                                try:
                                    status_code = int(parts[1])
                                except ValueError:
                                    pass
                        elif ": " in h:
                            k, v = h.split(": ", 1)
                            response_headers[k] = v

            elif etype == "URL_REQUEST_JOB_BYTES_READ":
                if "bytes" in params:
                    response_body_parts.append(params["bytes"])

            elif etype == "UPLOAD_DATA_STREAM_READ":
                if "bytes" in params:
                    request_body = params["bytes"]

        if not url or not is_line_domain(url):
            continue

        # Try to decode base64 body content
        response_body = "".join(response_body_parts) if response_body_parts else None

        entry = {
            "url": url,
            "method": method or "GET",
        }
        if status_code:
            entry["status"] = status_code
        if request_headers:
            # Filter to interesting headers
            interesting = {
                k: v for k, v in request_headers.items()
                if k.lower() in (
                    "content-type", "x-line-access", "x-line-application",
                    "x-lhm", "x-lpv", "x-lsr", "x-obs-params",
                    "x-line-channeltoken", "accept",
                )
            }
            if interesting:
                entry["request_headers"] = interesting
        if response_headers:
            interesting = {
                k: v for k, v in response_headers.items()
                if k.lower() in (
                    "content-type", "x-obs-oid", "x-line-next-access-token",
                    "x-line-error-code", "x-line-error-message",
                )
            }
            if interesting:
                entry["response_headers"] = interesting
        if request_body:
            entry["request_body_b64"] = request_body
        if response_body:
            entry["response_body_b64"] = response_body

        line_requests.append(entry)

    return {
        "total_events": len(events),
        "total_sources": len(sources),
        "line_requests_count": len(line_requests),
        "line_requests": line_requests,
    }


def categorize_requests(requests: list) -> dict:
    """Group requests by API category."""
    categories = defaultdict(list)
    for req in requests:
        url = req["url"]
        if "/api/talk/thrift/" in url:
            categories["thrift_rpc"].append(req)
        elif "/api/talk/long-polling/" in url or "/api/operation/receive" in url:
            categories["long_poll_sse"].append(req)
        elif "/api/auth/" in url:
            categories["auth"].append(req)
        elif "obs.line-apps.com" in url:
            categories["media_obs"].append(req)
        elif "stickershop" in url or "scdn" in url:
            categories["stickers_cdn"].append(req)
        elif "/sc/api/" in url or "legy" in url:
            categories["secondary_api"].append(req)
        else:
            categories["other"].append(req)
    return dict(categories)


def is_cdp_capture(data: dict) -> bool:
    """Check if the input is a CDP capture (vs net-log)."""
    return data.get("capture_method") == "cdp"


def parse_cdp_capture(data: dict) -> dict:
    """Parse a CDP capture file into the same format as parse_netlog."""
    requests = data.get("requests", [])
    sse_messages = data.get("sse_messages", [])

    line_requests = []
    for req in requests:
        url = req.get("url", "")
        entry = {
            "url": url,
            "method": req.get("method", "GET"),
        }
        if "status" in req:
            entry["status"] = req["status"]

        # Extract interesting request headers
        # requestWillBeSentExtraInfo carries the headers Chrome really sent
        # (Cookie, Origin, Sec-*, X-Line-*), which requestWillBeSent omits.
        headers = {**req.get("headers", {}), **req.get("request_extra_headers", {})}
        interesting_req = {}
        for k, v in headers.items():
            if k.lower() in (
                "content-type", "x-line-access", "x-line-application",
                "x-lhm", "x-lpv", "x-lsr", "x-obs-params",
                "x-line-channeltoken", "accept", "origin", "user-agent",
                "x-line-chrome-version", "x-line-mid",
            ) or k.lower().startswith("x-line-") or k.lower().startswith("x-ls"):
                interesting_req[k] = v
        if interesting_req:
            entry["request_headers"] = interesting_req

        # Extract interesting response headers
        resp_headers = req.get("response_headers", {})
        interesting_resp = {}
        for k, v in resp_headers.items():
            if k.lower() in (
                "content-type", "x-obs-oid", "x-line-next-access-token",
                "x-line-error-code", "x-line-error-message",
            ):
                interesting_resp[k] = v
        if interesting_resp:
            entry["response_headers"] = interesting_resp

        # Include response body if available
        if "response_body" in req:
            body_info = req["response_body"]
            body = body_info.get("body", "")
            if body_info.get("base64Encoded"):
                entry["response_body_b64"] = body
            else:
                # Try to parse as JSON for readability
                try:
                    entry["response_body"] = json.loads(body)
                except (json.JSONDecodeError, TypeError):
                    entry["response_body_text"] = body

        # Include request body if available
        if "request_body" in req:
            body = req["request_body"]
            if isinstance(body, dict):
                # Raw captures keep the bytes in base64; the parsed output only
                # keeps the text form and a size + sha256 for binary bodies.
                if body.get("error"):
                    entry["request_body_error"] = body["error"]
                else:
                    entry["request_body_size"] = body.get("size")
                    entry["request_body_sha256"] = body.get("sha256")
                    if body.get("text") is not None:
                        entry["request_body_text"] = body["text"]
            else:
                entry["request_body_text"] = body

        entry["timestamp"] = req.get("timestamp", "")
        line_requests.append(entry)

    return {
        "capture_method": "cdp",
        "total_requests": len(requests),
        "line_requests_count": len(line_requests),
        "sse_messages_count": len(sse_messages),
        "line_requests": line_requests,
        "sse_messages": sse_messages,
    }


def main():
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        sys.exit(1)

    input_path = sys.argv[1]
    output_path = sys.argv[2] if len(sys.argv) > 2 else None

    if not os.path.exists(input_path):
        print(f"File not found: {input_path}", file=sys.stderr)
        sys.exit(1)

    print(f"Reading {input_path}...", file=sys.stderr)
    with open(input_path, "r") as f:
        data = json.load(f)

    if is_cdp_capture(data):
        result = parse_cdp_capture(data)
    else:
        result = parse_netlog(input_path)

    # Add categorized view
    result["by_category"] = categorize_requests(result["line_requests"])

    # Summary
    print(f"\n=== LINE Traffic Summary ===", file=sys.stderr)
    print(f"Capture method: {result.get('capture_method', 'net-log')}", file=sys.stderr)
    print(f"LINE requests found: {result['line_requests_count']}", file=sys.stderr)
    if result.get("sse_messages_count"):
        print(f"SSE messages captured: {result['sse_messages_count']}", file=sys.stderr)
    for cat, reqs in result["by_category"].items():
        print(f"  {cat}: {len(reqs)}", file=sys.stderr)

    output_json = json.dumps(result, indent=2, ensure_ascii=False)

    if output_path:
        with open(output_path, "w") as f:
            f.write(output_json)
        print(f"\nOutput written to: {output_path}", file=sys.stderr)
    else:
        # Default output location
        default_out = input_path.replace(".json", "-parsed.json")
        with open(default_out, "w") as f:
            f.write(output_json)
        print(f"\nOutput written to: {default_out}", file=sys.stderr)


if __name__ == "__main__":
    main()
