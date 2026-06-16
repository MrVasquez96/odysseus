#!/usr/bin/env python3
"""Capture golden HTTP fixtures from a running Odysseus instance.

Exercises key endpoints and saves request/response pairs as JSON files
under go/testdata/fixtures/. These fixtures are the contract that the
Go proxy must preserve during migration.

Usage:
    # Start Odysseus first, then:
    python go/scripts/capture_fixtures.py [--base-url http://127.0.0.1:7000]

If auth is enabled, set ODYSSEUS_ADMIN_PASSWORD in the environment or
pass --password. The script will log in and use the session cookie.
"""

import argparse
import json
import os
import sys
import time

try:
    import httpx
except ImportError:
    print("httpx is required: pip install httpx")
    sys.exit(1)

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
FIXTURES_DIR = os.path.join(REPO_ROOT, "go", "testdata", "fixtures")


# Endpoints to capture (method, path, body, description)
FIXTURE_ENDPOINTS = [
    ("GET", "/api/health", None, "health_check"),
    ("GET", "/api/version", None, "version"),
    ("GET", "/api/auth/status", None, "auth_status"),
    ("GET", "/api/auth/features", None, "auth_features"),
    ("GET", "/", None, "index_html"),
    ("GET", "/login", None, "login_html"),
    ("GET", "/api/sessions", None, "sessions_list"),
    ("GET", "/api/ready", None, "readiness"),
    ("GET", "/api/runtime", None, "runtime_info"),
]

# Static file checks (verify cache headers)
STATIC_FIXTURES = [
    "/static/app.js",
    "/static/style.css",
    "/static/index.html",
]


def save_fixture(name, data):
    os.makedirs(FIXTURES_DIR, exist_ok=True)
    path = os.path.join(FIXTURES_DIR, f"{name}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, default=str)
    print(f"  Saved: {path}")


def capture_response(client, method, path, body=None):
    """Make a request and capture the full HTTP exchange."""
    kwargs = {"timeout": 30.0}
    if body:
        kwargs["json"] = body

    resp = getattr(client, method.lower())(path, **kwargs)

    # Serialize headers to dict (httpx Headers aren't directly JSON-able)
    resp_headers = dict(resp.headers)

    # Try to parse body as JSON, fall back to text
    try:
        resp_body = resp.json()
    except Exception:
        resp_body = resp.text[:2000] if len(resp.text) > 2000 else resp.text

    return {
        "request": {
            "method": method,
            "path": path,
            "body": body,
        },
        "response": {
            "status_code": resp.status_code,
            "headers": resp_headers,
            "body": resp_body,
        },
    }


def capture_sse_stream(client, path, body, max_seconds=30):
    """Capture an SSE stream transcript line by line."""
    lines = []
    try:
        with client.stream("POST", path, json=body, timeout=max_seconds) as resp:
            for line in resp.iter_lines():
                lines.append(line)
                # Stop after [DONE] sentinel
                if line.strip() == "data: [DONE]":
                    break
    except httpx.ReadTimeout:
        lines.append("# TIMEOUT after {}s".format(max_seconds))
    except Exception as e:
        lines.append(f"# ERROR: {e}")
    return lines


def login(client, base_url, username="admin", password="admin"):
    """Attempt to log in and return whether auth is active."""
    # Check auth status first
    try:
        resp = client.get("/api/auth/status")
        status = resp.json()
        if not status.get("auth_configured", True):
            print("Auth not configured, skipping login")
            return True
        if not status.get("auth_enabled", True):
            print("Auth disabled, skipping login")
            return True
    except Exception:
        pass

    # Try to log in
    resp = client.post("/api/auth/login", json={
        "username": username,
        "password": password,
    })
    if resp.status_code == 200:
        print(f"Logged in as {username}")
        return True
    else:
        print(f"Login failed ({resp.status_code}): {resp.text[:200]}")
        return False


def main():
    parser = argparse.ArgumentParser(description="Capture golden HTTP fixtures")
    parser.add_argument("--base-url", default="http://127.0.0.1:7000")
    parser.add_argument("--username", default="admin")
    parser.add_argument("--password", default=os.getenv("ODYSSEUS_ADMIN_PASSWORD", "admin"))
    parser.add_argument("--skip-sse", action="store_true", help="Skip SSE stream capture")
    args = parser.parse_args()

    os.makedirs(FIXTURES_DIR, exist_ok=True)

    client = httpx.Client(base_url=args.base_url, follow_redirects=False)

    # Login
    if not login(client, args.base_url, args.username, args.password):
        print("WARNING: Could not authenticate. Some fixtures may be 401/302.")

    # Capture standard endpoints
    print("\nCapturing endpoint fixtures...")
    for method, path, body, name in FIXTURE_ENDPOINTS:
        try:
            fixture = capture_response(client, method, path, body)
            save_fixture(name, fixture)
        except Exception as e:
            print(f"  FAILED {method} {path}: {e}")

    # Capture static file headers
    print("\nCapturing static file headers...")
    for static_path in STATIC_FIXTURES:
        try:
            fixture = capture_response(client, "GET", static_path)
            name = "static_" + static_path.split("/")[-1].replace(".", "_")
            save_fixture(name, fixture)
        except Exception as e:
            print(f"  FAILED GET {static_path}: {e}")

    # Capture security headers from a normal page
    print("\nCapturing security headers...")
    try:
        resp = client.get("/")
        headers_fixture = {
            "path": "/",
            "security_headers": {
                "X-Content-Type-Options": resp.headers.get("X-Content-Type-Options"),
                "Referrer-Policy": resp.headers.get("Referrer-Policy"),
                "Permissions-Policy": resp.headers.get("Permissions-Policy"),
                "X-Frame-Options": resp.headers.get("X-Frame-Options"),
                "Content-Security-Policy": resp.headers.get("Content-Security-Policy"),
                "Strict-Transport-Security": resp.headers.get("Strict-Transport-Security"),
            },
        }
        save_fixture("security_headers", headers_fixture)
    except Exception as e:
        print(f"  FAILED security headers: {e}")

    # Capture SSE stream
    if not args.skip_sse:
        print("\nCapturing SSE chat stream transcript...")
        try:
            sse_body = {
                "message": "Say exactly: Hello from golden fixture test",
                "session_id": None,
            }
            lines = capture_sse_stream(client, "/api/chat_stream", sse_body, max_seconds=60)
            transcript_path = os.path.join(FIXTURES_DIR, "chat_stream_transcript.txt")
            with open(transcript_path, "w", encoding="utf-8") as f:
                f.write("\n".join(lines))
            print(f"  Saved {len(lines)} SSE lines to {transcript_path}")
        except Exception as e:
            print(f"  FAILED SSE capture: {e}")

    # Summary
    fixture_files = [f for f in os.listdir(FIXTURES_DIR) if f.endswith(".json") or f.endswith(".txt")]
    print(f"\nDone. {len(fixture_files)} fixture files in {FIXTURES_DIR}")

    client.close()


if __name__ == "__main__":
    main()
