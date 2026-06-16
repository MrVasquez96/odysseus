# ── Stage 1: Build Go binary ──────────────────────────────────────────
# Phase 3+: CGO_ENABLED=1 for mattn/go-sqlite3 (gcc is in golang:bookworm).
FROM golang:1.24-bookworm AS go-builder
WORKDIR /build
COPY go/go.mod go/go.sum* ./go/
COPY go/cmd/ ./go/cmd/
COPY go/internal/ ./go/internal/
# Copy static/ into the embed location (go:embed can't follow symlinks).
COPY static/ ./go/cmd/odysseus/static/
RUN cd go && CGO_ENABLED=1 go build -trimpath -o /odysseus ./cmd/odysseus/

# ── Stage 2: Python + Go binary ──────────────────────────────────────
FROM python:3.12-slim

# System deps. tmux is required by Cookbook for background downloads/serves.
# openssh-client is required for Cookbook remote server tests, setup, probes,
# downloads, and serves from Docker installs.
# git/cmake are required when Cookbook builds llama.cpp on first llama.cpp
# launch inside Docker.
# nodejs/npm provide npx for the optional built-in Browser MCP server.
# gosu lets the entrypoint drop privileges cleanly so signals still reach
# the app directly (no extra shell layer like `su`/`sudo` would add).
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    curl \
    git \
    nodejs \
    npm \
    tmux \
    openssh-client \
    gosu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install Python deps first (layer cache). Optional extras (PyMuPDF AGPL, etc.)
# are opt-in so the default image stays MIT-core; see requirements-optional.txt.
ARG INSTALL_OPTIONAL=false
COPY requirements.txt requirements-optional.txt ./
RUN pip install --no-cache-dir -r requirements.txt \
    && if [ "$INSTALL_OPTIONAL" = "true" ]; then pip install --no-cache-dir -r requirements-optional.txt; fi

# Copy Go binary from build stage
COPY --from=go-builder /odysseus /usr/local/bin/odysseus

# Copy app code
COPY . .

# Create data directory (mount a volume here for persistence)
RUN mkdir -p data logs services/cache/search

# Entrypoint that drops to PUID/PGID (default 1000:1000) and repairs
# ownership on the bind-mounted /app/data and /app/logs. Without this,
# the container runs as root and writes root-owned files into host
# bind mounts — any later non-root run (or a host user trying to
# update them) silently fails on EPERM, breaking skill extraction,
# prefs persistence, mail attachments, etc.
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 7000

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
# Go serves :7000 (public), Python serves :7001 (internal).
# The entrypoint starts both processes.
CMD ["odysseus"]
