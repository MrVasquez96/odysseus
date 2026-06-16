# Odysseus Go Migration — Makefile
# Phase 0: baseline capture targets
# Phase 1+: build, test, run targets

REPO_ROOT := $(shell cd .. && pwd)
TESTDATA  := $(CURDIR)/testdata
FIXTURES  := $(TESTDATA)/fixtures
SCRIPTS   := $(CURDIR)/scripts

# Python (use the venv if it exists, otherwise system python)
PYTHON := $(shell test -d $(REPO_ROOT)/venv/bin && echo $(REPO_ROOT)/venv/bin/python || echo python3)

# Go build vars
BINARY   := odysseus
CGO      := 0
GOFLAGS  := -trimpath

# Embedded static directory (copied from repo root at build time).
# go:embed can't follow symlinks, so we rsync into the Go source tree.
EMBED_STATIC := $(CURDIR)/cmd/odysseus/static

.PHONY: help phase0 capture-routes capture-fixtures embed-static build run test clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

# ============ Phase 0 ============

phase0: capture-routes capture-fixtures ## Run all Phase 0 baseline captures
	@echo "Phase 0 complete. Fixtures in $(FIXTURES)"

capture-routes: ## Introspect FastAPI routes → testdata/route_manifest.json
	cd $(REPO_ROOT) && $(PYTHON) $(SCRIPTS)/capture_routes.py

capture-fixtures: ## Capture golden HTTP fixtures from running server
	cd $(REPO_ROOT) && $(PYTHON) $(SCRIPTS)/capture_fixtures.py

# ============ Phase 1+ ============

embed-static: ## Copy static/ into Go source tree for embedding
	@rsync -a --delete $(REPO_ROOT)/static/ $(EMBED_STATIC)/
	@echo "Static files synced to $(EMBED_STATIC)"

build: embed-static ## Build the Go binary (copies static/ first)
	cd $(CURDIR) && CGO_ENABLED=$(CGO) go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/odysseus/

run: build ## Build and run the Go binary
	cd $(REPO_ROOT) && $(CURDIR)/bin/$(BINARY)

test: ## Run Go tests
	cd $(CURDIR) && go test ./...

clean: ## Remove build artifacts and embedded static copy
	rm -rf $(CURDIR)/bin/
	rm -rf $(EMBED_STATIC)

# ============ Development ============

dev: embed-static ## Run Go binary in dev mode (Python on :7001 must be running separately)
	cd $(CURDIR) && ODYSSEUS_PYTHON_ADDR=http://127.0.0.1:7001 go run ./cmd/odysseus/

lint: ## Run Go linter
	cd $(CURDIR) && go vet ./...
