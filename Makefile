# grove — build and test targets.
#
# The binary's version is stamped at link time from `git describe`. When the
# tree isn't a git checkout (e.g. a source tarball), VERSION falls back to
# 0.0.0-dev, matching the default in internal/cli.Version. Override with
# `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X grove/internal/cli.Version=$(VERSION)

WEB := internal/adapters/http/web

# Workspace served by `make dev` (pass WS=/path/to/forest to override). Empty
# uses grove's default workspace (~/.grove/default or $GROVE_WORKSPACE).
WS ?=

.PHONY: build test fmt clean web web-dev dev

build:
	go build -ldflags "$(LDFLAGS)" -o bin/grove ./cmd/grove

# web rebuilds the React SPA into $(WEB)/dist, which is committed and embedded
# into the binary via //go:embed. Run this ONLY when the frontend changes —
# it is deliberately NOT a dependency of `build`, so `go build` never needs a
# JavaScript toolchain and the single-binary property holds.
web:
	cd $(WEB) && npm install && npm run build

# dev runs the full frontend-dev stack in one command: it (re)builds the grove
# binary, lists the available workspaces for you to pick, then starts the
# backend on :8799 and the Vite dev server on :5173 (hot reload, proxies /api to
# the backend). Ctrl-C stops both. Open http://localhost:5173. Pass
# WS=/path/to/forest to skip the picker; GROVE_WS_ROOTS=/a:/b to change where it
# looks for workspaces.
dev: build
	@bash scripts/dev.sh $(WS)

# web-dev runs ONLY the Vite dev server (hot reload) on :5173. It proxies /api
# to a grove backend on :8799 that you must start yourself — otherwise every
# /api call 500s. Prefer `make dev`, which starts both.
web-dev:
	@echo "→ Vite dev server on http://localhost:5173 (frontend hot-reload only)."
	@echo "→ REQUIRES a backend on :8799 — run \`make dev\` instead to start both,"
	@echo "  or in another terminal: grove serve --web --port 8799 --no-open"
	@echo ""
	cd $(WEB) && npm run dev

test:
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
