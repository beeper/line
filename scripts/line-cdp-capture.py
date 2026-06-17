#!/usr/bin/env python3
"""Capture LINE traffic via Chrome DevTools Protocol.

Connects to Chrome's remote debugging port and captures:
- SSE EventSource messages (the critical piece net-log misses)
- Thrift RPC request/response bodies
- All LINE-domain request headers

Usage:
    # First, launch Chrome with --remote-debugging-port=9222
    # Then:
    python3 scripts/line-cdp-capture.py [output.json]

    # Or use the wrapper:
    bash scripts/line-chrome-capture.sh [output_dir]
"""

import json
import sys
import os
import signal
import time
import base64
import threading
from collections import defaultdict
from datetime import datetime, timezone
from urllib.parse import urlparse

import websocket

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

CDP_PORT = 9222


def is_line_url(url: str) -> bool:
    for domain in LINE_DOMAINS:
        if domain in url:
            return True
    return False


def timestamp() -> str:
    return datetime.now(timezone.utc).isoformat()


class CDPCapture:
    def __init__(self, output_path: str):
        self.output_path = output_path
        self.ws = None
        self.msg_id = 0
        self.lock = threading.Lock()

        # Captured data
        self.requests = {}  # requestId -> request info
        self.sse_messages = []  # EventSource messages
        self.thrift_responses = {}  # requestId -> response body
        self.request_bodies = {}  # requestId -> request body
        self.pending_body_requests = {}  # msg_id -> (requestId, session_id)
        self.sessions = {}  # sessionId -> targetInfo

        self.running = True

    def next_id(self) -> int:
        with self.lock:
            self.msg_id += 1
            return self.msg_id

    def send(self, method: str, params: dict = None) -> int:
        mid = self.next_id()
        msg = {"id": mid, "method": method}
        if params:
            msg["params"] = params
        self.ws.send(json.dumps(msg))
        return mid

    def connect(self):
        """Connect to Chrome CDP browser target and auto-attach to all targets.

        Extensions run in their own target (service_worker/background_page),
        so we connect at the browser level and use Target.setAutoAttach to
        intercept network events from every target including extensions.
        """
        import urllib.request

        # Get browser-level WebSocket URL
        try:
            resp = urllib.request.urlopen(f"http://127.0.0.1:{CDP_PORT}/json/version")
            version_info = json.loads(resp.read())
            ws_url = version_info["webSocketDebuggerUrl"]
        except Exception as e:
            print(f"Cannot connect to Chrome CDP on port {CDP_PORT}: {e}", file=sys.stderr)
            print("Make sure Chrome is running with --remote-debugging-port=9222", file=sys.stderr)
            sys.exit(1)

        # Also list targets for debugging
        try:
            resp = urllib.request.urlopen(f"http://127.0.0.1:{CDP_PORT}/json")
            targets = json.loads(resp.read())
            for t in targets:
                print(f"  Target: [{t.get('type')}] {t.get('title', '?')} ({t.get('url', '')})", file=sys.stderr)
        except Exception:
            pass

        print(f"Connecting to browser target: {ws_url}", file=sys.stderr)

        self.ws = websocket.WebSocket()
        self.ws.connect(ws_url)

        # Auto-attach to ALL targets (pages, extensions, service workers, etc.)
        # This makes Network events from extension contexts flow through our connection.
        self.send("Target.setAutoAttach", {
            "autoAttach": True,
            "waitForDebuggerOnStart": False,
            "flatten": True,
        })

        # Also discover existing targets
        self.send("Target.setDiscoverTargets", {
            "discover": True,
        })

        print("CDP connected to browser. Waiting for targets...", file=sys.stderr)

    def send_to_session(self, session_id: str, method: str, params: dict = None) -> int:
        """Send a CDP command to a specific session (attached target)."""
        mid = self.next_id()
        msg = {"id": mid, "method": method, "sessionId": session_id}
        if params:
            msg["params"] = params
        self.ws.send(json.dumps(msg))
        return mid

    def handle_event(self, msg: dict):
        method = msg.get("method", "")
        params = msg.get("params", {})
        session_id = msg.get("sessionId", "")

        # Handle target attachment — enable Network on each new target
        if method == "Target.attachedToTarget":
            target_info = params.get("targetInfo", {})
            sid = params.get("sessionId", "")
            ttype = target_info.get("type", "")
            title = target_info.get("title", "")
            url = target_info.get("url", "")
            print(f"  Attached to [{ttype}] {title} ({url})", file=sys.stderr)

            # Enable Network capture on this target
            self.send_to_session(sid, "Network.enable", {
                "maxTotalBufferSize": 100 * 1024 * 1024,
                "maxResourceBufferSize": 10 * 1024 * 1024,
            })
            # Track session -> target mapping
            self.sessions[sid] = target_info
            return

        if method == "Target.detachedFromTarget":
            sid = params.get("sessionId", "")
            target = self.sessions.pop(sid, {})
            print(f"  Detached from [{target.get('type', '?')}] {target.get('title', '?')}", file=sys.stderr)
            return

        if method == "Network.requestWillBeSent":
            self._on_request(params, session_id)
        elif method == "Network.responseReceived":
            self._on_response(params, session_id)
        elif method == "Network.loadingFinished":
            self._on_loading_finished(params, session_id)
        elif method == "Network.eventSourceMessageReceived":
            self._on_sse_message(params)
        elif method == "Network.dataReceived":
            # Track streaming data chunks
            pass

    def handle_response(self, msg: dict):
        mid = msg.get("id")
        if mid in self.pending_body_requests:
            request_id, session_id = self.pending_body_requests.pop(mid)
            result = msg.get("result", {})
            body = result.get("body", "")
            is_b64 = result.get("base64Encoded", False)
            if body:
                self.thrift_responses[request_id] = {
                    "body": body,
                    "base64Encoded": is_b64,
                    "timestamp": timestamp(),
                }

    def _on_request(self, params: dict, session_id: str = ""):
        request = params.get("request", {})
        url = request.get("url", "")
        if not is_line_url(url):
            return

        request_id = params.get("requestId")
        headers = request.get("headers", {})

        self.requests[request_id] = {
            "url": url,
            "method": request.get("method", "GET"),
            "headers": headers,
            "timestamp": timestamp(),
            "has_post_data": request.get("hasPostData", False),
            "session_id": session_id,
        }

        # Capture POST body for Thrift calls
        if request.get("hasPostData") and "/thrift/" in url:
            post_data = request.get("postData", "")
            if post_data:
                self.request_bodies[request_id] = post_data

        count = len(self.requests)
        if count % 10 == 1:
            print(f"  [{count} LINE requests captured]", file=sys.stderr)

    def _on_response(self, params: dict, session_id: str = ""):
        request_id = params.get("requestId")
        if request_id not in self.requests:
            return

        response = params.get("response", {})
        self.requests[request_id]["status"] = response.get("status")
        self.requests[request_id]["response_headers"] = response.get("headers", {})
        self.requests[request_id]["mime_type"] = response.get("mimeType", "")

    def _on_loading_finished(self, params: dict, session_id: str = ""):
        request_id = params.get("requestId")
        if request_id not in self.requests:
            return

        req = self.requests[request_id]
        url = req.get("url", "")

        # Fetch response body for Thrift RPC and JSON responses
        # Must use the same session that owns the request
        target_session = req.get("session_id", session_id)
        if "/thrift/" in url or req.get("mime_type", "") == "application/json":
            if target_session:
                mid = self.send_to_session(target_session, "Network.getResponseBody", {"requestId": request_id})
            else:
                mid = self.send("Network.getResponseBody", {"requestId": request_id})
            self.pending_body_requests[mid] = (request_id, target_session)

    def _on_sse_message(self, params: dict):
        """This is the key event — captures each SSE message as it arrives."""
        request_id = params.get("requestId")
        url = ""
        if request_id in self.requests:
            url = self.requests[request_id].get("url", "")

        event_name = params.get("eventName", "")
        event_id = params.get("eventId", "")
        data = params.get("data", "")

        entry = {
            "timestamp": timestamp(),
            "requestId": request_id,
            "url": url,
            "eventName": event_name,
            "eventId": event_id,
            "data": data,
        }

        # Try to parse the data as JSON
        try:
            entry["parsed"] = json.loads(data)
        except (json.JSONDecodeError, TypeError):
            pass

        self.sse_messages.append(entry)

        # Log it live
        preview = data[:120] if data else "(empty)"
        print(f"  SSE [{event_name or 'message'}]: {preview}", file=sys.stderr)

    def save(self):
        """Write captured data to output file."""
        # Build structured output
        requests_list = []
        for rid, req in self.requests.items():
            entry = dict(req)
            entry.pop("session_id", None)  # internal, don't export
            if rid in self.thrift_responses:
                entry["response_body"] = self.thrift_responses[rid]
            if rid in self.request_bodies:
                entry["request_body"] = self.request_bodies[rid]
            requests_list.append(entry)

        output = {
            "capture_method": "cdp",
            "capture_time": timestamp(),
            "requests_count": len(requests_list),
            "sse_messages_count": len(self.sse_messages),
            "requests": requests_list,
            "sse_messages": self.sse_messages,
        }

        with open(self.output_path, "w") as f:
            json.dump(output, f, indent=2, ensure_ascii=False)

        print(f"\nCapture saved to: {self.output_path}", file=sys.stderr)
        print(f"  Requests: {len(requests_list)}", file=sys.stderr)
        print(f"  SSE messages: {len(self.sse_messages)}", file=sys.stderr)

    def run(self):
        """Main event loop — read CDP messages until Chrome closes or Ctrl+C."""
        print("Listening for LINE traffic... (Ctrl+C or close Chrome to stop)\n", file=sys.stderr)

        while self.running:
            try:
                raw = self.ws.recv()
                if not raw:
                    break
                msg = json.loads(raw)

                if "method" in msg:
                    self.handle_event(msg)
                elif "id" in msg:
                    self.handle_response(msg)

            except websocket.WebSocketConnectionClosedException:
                print("\nChrome closed connection.", file=sys.stderr)
                break
            except KeyboardInterrupt:
                print("\nStopping capture...", file=sys.stderr)
                break
            except Exception as e:
                print(f"Error: {e}", file=sys.stderr)
                continue

        self.save()


def wait_for_chrome(timeout: int = 60) -> bool:
    """Wait for Chrome CDP to become available."""
    import urllib.request

    print(f"Waiting for Chrome CDP on port {CDP_PORT}...", file=sys.stderr)
    start = time.time()
    while time.time() - start < timeout:
        try:
            resp = urllib.request.urlopen(f"http://127.0.0.1:{CDP_PORT}/json", timeout=2)
            targets = json.loads(resp.read())
            if targets:
                return True
        except Exception:
            pass
        time.sleep(1)
    return False


def main():
    output_path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/line-chrome-extension-capture/line-cdp-capture.json"

    # Ensure output directory exists
    os.makedirs(os.path.dirname(output_path), exist_ok=True)

    if not wait_for_chrome():
        print("Timed out waiting for Chrome. Is it running with --remote-debugging-port=9222?", file=sys.stderr)
        sys.exit(1)

    capture = CDPCapture(output_path)

    # Handle SIGTERM gracefully
    def handle_signal(signum, frame):
        capture.running = False
    signal.signal(signal.SIGTERM, handle_signal)

    capture.connect()
    capture.run()


if __name__ == "__main__":
    main()
