#!/bin/sh
# Entrypoint that fixes the #1 self-host footgun: a Docker container
# that runs as root writes root-owned files into bind-mounted host
# volumes, and the host user (or a non-root service user) then can't
# update them — silently breaking skill extraction, prefs saves, mail
# attachments, etc.
#
# Standard PUID/PGID pattern: pick the UID/GID we should drop to,
# chown the writable bind-mounts so existing root-owned content gets
# repaired on every start (idempotent), then exec the real command
# as that user via gosu.
set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# Reuse an existing matching group/user if the host's UID/GID already
# corresponds to one in /etc/passwd (e.g. when the image is rebuilt
# and "odysseus" already exists at the same id). Otherwise create.
if ! getent group "$PGID" >/dev/null 2>&1; then
    groupadd -g "$PGID" odysseus
fi
if ! getent passwd "$PUID" >/dev/null 2>&1; then
    useradd -u "$PUID" -g "$PGID" -M -s /bin/sh -d /app odysseus
fi

# Repair ownership on every writable path the app touches at runtime.
#
# Bind-mounted dirs (/app/data, /app/logs) are the obvious ones, but
# the app ALSO writes inside the image's own source tree at runtime:
#   - services/cache/{search,content}/*  (search cache LRU)
#   - services/search_analytics.json
#   - services/search_engine_error.log
#   - services/tts cache, etc.
# These dirs were created as root during `docker build`, so dropping
# to PUID:PGID would otherwise crash on the first import that tries
# to mkdir them. Chown the whole /app tree — fast (<1s on this size)
# and idempotent via the `-not -uid` filter so we only touch files
# that need fixing.
for dir in /app /app/data /app/logs; do
    if [ -d "$dir" ]; then
        # `find ... -not -uid` keeps this O(touched-files), not
        # O(everything), so terabyte-sized maildirs don't slow startup.
        find "$dir" -not -uid "$PUID" -print0 2>/dev/null \
            | xargs -0 -r chown "$PUID:$PGID" 2>/dev/null || true
    fi
done

# Cookbook installs vllm/etc. via `pip install --user`, which pulls
# nvidia-cuda-* wheels into /app/.local but does not set CUDA_HOME or
# symlink /usr/local/cuda. vllm 0.22+ then crashes during engine init
# when FlashInfer tries to JIT a sampler kernel ("Could not find nvcc",
# then "CUDA compiler and toolkit headers are incompatible" on the
# mixed cuda-nvcc 13.3 / cuda-runtime 13.0 wheel combo).
#
# Auto-set CUDA_HOME if a pip-installed nvcc is present, and disable the
# FlashInfer JIT sampler — sampler only, no impact on attention path.
# No-op when vllm isn't installed.
#
# Checked layouts (all are real pip-wheel install paths):
#   nvidia/cu13        — nvidia-nvcc-cu13 (CUDA 13.x wheel style)
#   nvidia/cu12        — nvidia-nvcc-cu12 (CUDA 12.x wheel style)
#   nvidia/cuda_nvcc   — nvidia-cuda-nvcc-cu12 (older cu12 sub-package style)
for cu in \
    /app/.local/lib/python*/site-packages/nvidia/cu13 \
    /app/.local/lib/python*/site-packages/nvidia/cu12 \
    /app/.local/lib/python*/site-packages/nvidia/cuda_nvcc; do
    if [ -x "$cu/bin/nvcc" ]; then
        export CUDA_HOME="$cu"
        break
    fi
done
# Disable the FlashInfer JIT sampler unconditionally — it is sampler-only
# and has no impact on the attention path, but requires nvcc + matching
# CUDA headers at startup. Without this, vLLM crashes with "Could not find
# nvcc" even when the GPU itself is fully visible to the container.
export VLLM_USE_FLASHINFER_SAMPLER="${VLLM_USE_FLASHINFER_SAMPLER:-0}"

# Make Cookbook-installed Python CLIs visible after `pip install --user`.
# vLLM and helper scripts land here because /app is the non-root user's HOME.
export PATH="/app/.local/bin:$PATH"

# Run first-time setup as the app user so data/ files get the right ownership.
# setup.py is idempotent — skips auth.json / .env if they already exist.
# || true so a setup failure never prevents the container from starting.
gosu "$PUID:$PGID" python /app/setup.py || true

# ── Dual-process mode (Go + Python) or legacy single-process ──────────
# When CMD is "odysseus", start the Go binary as the public-facing
# server on :7000 and Python (uvicorn) as the internal ML worker on
# :7001. Go serves static files and proxies API requests to Python.
#
# When CMD is "uvicorn ..." (legacy or explicit override), run Python
# directly on :7000 as before — no Go binary involved.

if [ "$1" = "odysseus" ]; then
    # Shared token for internal-tool calls (file-based so both read it).
    python -c 'import secrets; print(secrets.token_hex(32))' > /app/data/.internal_token
    chown "$PUID:$PGID" /app/data/.internal_token

    # Start Python ML worker on :7001 (loopback only, not exposed).
    # Phase 2+: Go owns auth at the public :7000 boundary. Python on
    # :7001 is loopback-only, so we disable its auth middleware via an
    # inline env override. This does NOT export AUTH_ENABLED globally —
    # Go must still see the real AUTH_ENABLED value from docker-compose.
    AUTH_ENABLED=false gosu "$PUID:$PGID" uvicorn app:app --host 127.0.0.1 --port 7001 &
    PYTHON_PID=$!

    # Wait for Python to be ready before starting Go.
    echo "Waiting for Python worker on :7001..."
    for i in $(seq 1 30); do
        if curl -sf http://127.0.0.1:7001/api/health >/dev/null 2>&1; then
            echo "Python worker ready."
            break
        fi
        sleep 1
    done

    # Start Go server on :7000 (public). Sets ODYSSEUS_PYTHON_ADDR
    # so the Go proxy knows where to forward API requests.
    # Also sets ODYSSEUS_INTERNAL_BASE so Python agent loopback calls
    # route through Go (needed once Go owns auth in Phase 2+).
    export ODYSSEUS_PYTHON_ADDR="http://127.0.0.1:7001"
    export ODYSSEUS_INTERNAL_BASE="http://127.0.0.1:7000"
    gosu "$PUID:$PGID" /usr/local/bin/odysseus &
    GO_PID=$!

    # ── Optional: LiveKit voice agent ───────────────────────────────
    # Start the Python voice agent if enabled and LiveKit deps are installed.
    VOICE_PID=""
    if [ "${VOICE_AGENT_ENABLED:-false}" = "true" ]; then
        if python -c "import livekit; import livekit.agents" 2>/dev/null; then
            echo "Starting voice agent (LiveKit)..."
            LIVEKIT_URL="${LIVEKIT_URL:-ws://livekit:7880}" \
            LIVEKIT_API_KEY="${LIVEKIT_API_KEY:-devkey}" \
            LIVEKIT_API_SECRET="${LIVEKIT_API_SECRET:-devsecret1234567890abcdef1234567890abcdef}" \
            VOICE_MODE="${VOICE_MODE:-local}" \
            VOICE_LLM_ENDPOINT="${VOICE_LLM_ENDPOINT:-}" \
            VOICE_LLM_MODEL="${VOICE_LLM_MODEL:-}" \
            VOICE_LLM_API_KEY="${VOICE_LLM_API_KEY:-not-needed}" \
            VOICE_STT_ENDPOINT="${VOICE_STT_ENDPOINT:-http://whisper:8000/v1}" \
            VOICE_TTS_ENDPOINT="${VOICE_TTS_ENDPOINT:-http://openedai-speech:8000/v1}" \
            VOICE_TTS_MODEL="${VOICE_TTS_MODEL:-tts-1}" \
            VOICE_TTS_VOICE="${VOICE_TTS_VOICE:-alloy}" \
            GOOGLE_API_KEY="${GOOGLE_API_KEY:-}" \
            OLLAMA_BASE_URL="${OLLAMA_BASE_URL:-}" \
            INTERNAL_TOKEN_PATH="/app/data/.internal_token" \
            gosu "$PUID:$PGID" python /app/services/voice/agent.py start &
            VOICE_PID=$!
        else
            echo "VOICE_AGENT_ENABLED=true but livekit not installed — skipping voice agent."
            echo "Install with: pip install livekit livekit-agents[google] httpx"
        fi
    fi

    # Trap SIGTERM/SIGINT to stop all processes on container shutdown.
    trap 'kill $PYTHON_PID $GO_PID $VOICE_PID 2>/dev/null' TERM INT

    # Wait for either core process to exit (POSIX-compatible, no `wait -n`).
    # Poll both PIDs — when one dies, kill the other and exit.
    while kill -0 $PYTHON_PID 2>/dev/null && kill -0 $GO_PID 2>/dev/null; do
        wait $PYTHON_PID $GO_PID 2>/dev/null || break
    done
    kill $PYTHON_PID $GO_PID $VOICE_PID 2>/dev/null
    wait $PYTHON_PID 2>/dev/null
    wait $GO_PID 2>/dev/null
    [ -n "$VOICE_PID" ] && wait $VOICE_PID 2>/dev/null
    exit 0
else
    # Legacy mode: run whatever CMD was passed (e.g. uvicorn) directly.
    exec gosu "$PUID:$PGID" "$@"
fi
