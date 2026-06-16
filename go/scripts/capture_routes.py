#!/usr/bin/env python3
"""Capture the full route manifest from the running Odysseus FastAPI app.

Introspects app.routes and writes a machine-readable JSON manifest to
go/testdata/route_manifest.json. Each entry records path, methods, name,
and whether it returns SSE (text/event-stream).

Usage:
    cd /path/to/odysseus
    python go/scripts/capture_routes.py
"""

import json
import os
import sys
import inspect

# Ensure the repo root is on the path so we can import app.
# When running inside the Docker container, __file__ is /tmp/capture_routes.py
# so we fall back to /app (the container's app root).
REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
if not os.path.isfile(os.path.join(REPO_ROOT, "app.py")):
    REPO_ROOT = "/app"
sys.path.insert(0, REPO_ROOT)
os.chdir(REPO_ROOT)

from app import app
from starlette.routing import Route, Mount


def _is_sse_endpoint(route: Route) -> bool:
    """Heuristically detect SSE endpoints by inspecting their source."""
    if not hasattr(route, "endpoint"):
        return False
    try:
        source = inspect.getsource(route.endpoint)
        return 'media_type="text/event-stream"' in source or "text/event-stream" in source
    except (OSError, TypeError):
        return False


def _collect_routes(routes, prefix=""):
    """Recursively collect routes from the app router."""
    entries = []
    for route in routes:
        if isinstance(route, Mount):
            mount_path = prefix + route.path.rstrip("/")
            if hasattr(route, "routes"):
                entries.extend(_collect_routes(route.routes, prefix=mount_path))
            else:
                entries.append({
                    "path": mount_path + "/{path:path}",
                    "methods": ["GET"],
                    "name": route.name or "",
                    "is_sse": False,
                    "type": "mount",
                })
        elif isinstance(route, Route):
            path = prefix + route.path
            methods = sorted(route.methods - {"HEAD", "OPTIONS"}) if route.methods else ["GET"]
            entries.append({
                "path": path,
                "methods": methods,
                "name": route.name or "",
                "is_sse": _is_sse_endpoint(route),
                "type": "route",
            })
    return entries


def main():
    routes = _collect_routes(app.routes)

    # Sort by path for stable output
    routes.sort(key=lambda r: (r["path"], r["methods"]))

    manifest = {
        "total_routes": len(routes),
        "sse_endpoints": [r["path"] for r in routes if r["is_sse"]],
        "routes": routes,
    }

    # Write to go/testdata if it exists, otherwise to stdout
    out_dir = os.path.join(REPO_ROOT, "go", "testdata")
    if os.path.isdir(out_dir):
        out_path = os.path.join(out_dir, "route_manifest.json")
        with open(out_path, "w", encoding="utf-8") as f:
            json.dump(manifest, f, indent=2)
        print(f"Captured {len(routes)} routes ({len(manifest['sse_endpoints'])} SSE)",
              file=sys.stderr)
        print(f"Written to {out_path}", file=sys.stderr)
    else:
        # Running inside Docker — dump to stdout for piping
        json.dump(manifest, sys.stdout, indent=2)
        print(f"\nCaptured {len(routes)} routes ({len(manifest['sse_endpoints'])} SSE)",
              file=sys.stderr)

    # Summary
    route_types = {}
    for r in routes:
        for m in r["methods"]:
            route_types[m] = route_types.get(m, 0) + 1
    for method, count in sorted(route_types.items()):
        print(f"  {method}: {count}", file=sys.stderr)


if __name__ == "__main__":
    main()
