#!/usr/bin/env python3
"""Minimal octo-server stub for local plugin-review development.

Implements exactly the two internal endpoints internal/notify.Client calls:

  GET  /v1/internal/spaces/{space_id}/members/{uid}/role
       -> {"data": {"role": 2|1|0|null}}          (notify.memberRoleEnvelope)
  POST /v1/internal/notify
       -> {"data": {"delivered": ["uid", ...], "filtered": {"uid": "reason"}}}
                                                  (notify.notifyEnvelope)

The response SHAPES above are load-bearing, not illustrative. internal/notify
decodes `delivered` as []string and `filtered` as map[string]string, so returning
counts (as an earlier version of this stub did) makes json.Unmarshal fail on
every dispatch and the caller logs `notify_best_effort_failed` — a stub bug that
reads exactly like a bug in the service. Likewise the request body carries the
card under `approval_card`, not `card`.

Both require the X-Internal-Token header from OCTO_MARKETPLACE_INTERNAL_TOKEN, so an
unauthenticated caller sees the same 401 the real service returns.

The role table is configurable, which is the point of the stub: the card-action
callback deliberately re-derives the operator's role from octo-server instead of
trusting the signed operator_uid, so testing that authorization needs a server
that can answer "this uid is/isn't an admin here".

  DEV_STUB_ROLES="dev-user:2,someone:0"   # uid:role, role 0=member 1=admin 2=owner
  DEV_STUB_PORT=18777

Any uid not listed answers role=null (not a member) — the same byte-identical
response the real endpoint gives for a removed member, an unknown user, or a
disbanded Space, so a caller cannot probe membership.
"""

import json
import os
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote

PORT = int(os.environ.get("DEV_STUB_PORT", "18777"))
TOKEN = os.environ.get("OCTO_MARKETPLACE_INTERNAL_TOKEN", "").strip()

ROLES = {}
for entry in os.environ.get("DEV_STUB_ROLES", "dev-user:2").split(","):
    entry = entry.strip()
    if not entry:
        continue
    uid, _, role = entry.partition(":")
    ROLES[uid.strip()] = int(role or 0)

ROLE_PATH = re.compile(r"^/v1/internal/spaces/([^/]+)/members/([^/]+)/role/?$")

# The only target_role the real endpoint accepts (notify.targetRoleSpaceAdmin).
TARGET_ROLE = "space_admin"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):  # silence the default access log
        pass

    def _send(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _authorized(self):
        # The real endpoints are gated by X-Internal-Token, not a bearer token.
        if not TOKEN:
            return True
        if self.headers.get("X-Internal-Token", "") == TOKEN:
            return True
        self._send(401, {"error": {"code": "AUTH_REQUIRED", "message": "bad internal token"}})
        return False

    def do_GET(self):
        match = ROLE_PATH.match(self.path)
        if not match:
            self._send(404, {"error": {"code": "NOT_FOUND", "message": self.path}})
            return
        if not self._authorized():
            return
        space_id, uid = unquote(match.group(1)), unquote(match.group(2))
        role = ROLES.get(uid)
        print(f"[stub] member role: space={space_id} uid={uid} -> {role}", flush=True)
        self._send(200, {"data": {"role": role}})

    def do_POST(self):
        if not self.path.rstrip("/").endswith("/v1/internal/notify"):
            self._send(404, {"error": {"code": "NOT_FOUND", "message": self.path}})
            return
        if not self._authorized():
            return
        length = int(self.headers.get("Content-Length") or 0)
        req = json.loads(self.rfile.read(length) or b"{}")
        # The real service rejects target_role and targets together; surface
        # that here rather than silently accepting a roster. (notifyWire has no
        # Targets field, so sending one is already a client bug.)
        if "targets" in req:
            self._send(400, {"error": {"code": "VALIDATION_ERROR", "message": "targets forbidden with target_role"}})
            return
        if req.get("target_role") != TARGET_ROLE:
            self._send(400, {"error": {"code": "VALIDATION_ERROR", "message": "unknown target_role"}})
            return
        card = req.get("approval_card") or {}
        admins = [uid for uid, role in ROLES.items() if role >= 1]
        print(f"[stub] notify: space={req.get('space_id')} target_role={req.get('target_role')!r} actor={req.get('actor_uid')!r}", flush=True)
        print(f"[stub]   action_type: {card.get('action_type')!r}", flush=True)
        print(f"[stub]   title:       {card.get('title')!r}", flush=True)
        print(f"[stub]   description: {card.get('description')!r}", flush=True)
        print(f"[stub]   data:        {card.get('data')}", flush=True)
        print(f"[stub]   delivered={admins} filtered={{}}", flush=True)
        # Shapes must match notifyEnvelope exactly: delivered []string,
        # filtered map[string]string. The Go client treats these as required
        # types; returning ints here makes json.Unmarshal fail every time.
        self._send(200, {"data": {"delivered": admins, "filtered": {}}})


if __name__ == "__main__":
    print(f"[stub] listening on :{PORT}  roles={ROLES}  token={'set' if TOKEN else 'OFF'}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
