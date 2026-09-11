#!/usr/bin/env python3
"""Start the development Octo stub and verify its split-token contract."""

import json
import os
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


ROLE_TOKEN = "role-token-for-dev-stub-smoke-test"
NOTIFY_TOKEN = "notify-token-for-dev-stub-smoke-test"


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(url, method="GET", token=ROLE_TOKEN, payload=None):
    body = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("X-Internal-Token", token)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=1) as response:
        return response.status, json.load(response)


def wait_for_role(url, process):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("stub exited before accepting requests")
        try:
            return request(url)
        except (OSError, urllib.error.URLError):
            time.sleep(0.05)
    raise RuntimeError("stub did not accept requests within 5 seconds")


def main():
    port = available_port()
    env = os.environ.copy()
    env.update(
        {
            "DEV_STUB_PORT": str(port),
            "DEV_STUB_ROLES": "admin-1:2",
            "OCTO_MARKETPLACE_INTERNAL_TOKEN": ROLE_TOKEN,
            "OCTO_MARKETPLACE_NOTIFY_TOKEN": NOTIFY_TOKEN,
        }
    )
    script = Path(__file__).with_name("dev-octo-stub.py")
    process = subprocess.Popen(
        [sys.executable, str(script)],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    output = ""
    try:
        base_url = f"http://127.0.0.1:{port}"
        status, role = wait_for_role(
            f"{base_url}/v1/internal/spaces/space-a/members/admin-1/role",
            process,
        )
        if status != 200 or role != {"data": {"role": 2}}:
            raise AssertionError(f"unexpected role response: {status} {role!r}")

        status, notify = request(
            f"{base_url}/v1/internal/notify",
            method="POST",
            token=NOTIFY_TOKEN,
            payload={
                "space_id": "space-a",
                "target_role": "space_admin",
                "approval_card": {"action_type": "review", "title": "Review"},
            },
        )
        if status != 200 or notify["data"]["delivered"] != ["admin-1"]:
            raise AssertionError(f"unexpected notify response: {status} {notify!r}")

        try:
            request(
                f"{base_url}/v1/internal/notify",
                method="POST",
                token=ROLE_TOKEN,
                payload={
                    "space_id": "space-a",
                    "target_role": "space_admin",
                    "approval_card": {"action_type": "review", "title": "Review"},
                },
            )
        except urllib.error.HTTPError as err:
            if err.code != 401:
                raise
        else:
            raise AssertionError("notify endpoint accepted the role token")
    finally:
        process.terminate()
        try:
            output = process.communicate(timeout=2)[0]
        except subprocess.TimeoutExpired:
            process.kill()
            output = process.communicate()[0]

    if "role_token=set" not in output or "notify_token=set" not in output:
        raise AssertionError(f"startup banner did not report both credentials:\n{output}")

    partial_env = os.environ.copy()
    partial_env.update(
        {
            "DEV_STUB_PORT": str(available_port()),
            "OCTO_MARKETPLACE_INTERNAL_TOKEN": ROLE_TOKEN,
            "OCTO_MARKETPLACE_NOTIFY_TOKEN": "",
        }
    )
    partial = subprocess.run(
        [sys.executable, str(script)],
        env=partial_env,
        capture_output=True,
        text=True,
        timeout=2,
        check=False,
    )
    partial_output = partial.stdout + partial.stderr
    if partial.returncode == 0 or "configure both" not in partial_output:
        raise AssertionError(
            "stub did not reject a partial token configuration:\n"
            f"stdout:\n{partial.stdout}\nstderr:\n{partial.stderr}"
        )


if __name__ == "__main__":
    main()
