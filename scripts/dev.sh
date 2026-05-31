#!/usr/bin/env bash
# Interactive dev launcher: discover grove workspaces, pick one, then run the
# backend (:8799) and the Vite dev server (:5173) together. Ctrl-C stops both.
#
# Usage:
#   scripts/dev.sh [workspace-path]      # arg skips the picker
#   GROVE_WS_ROOTS=/a:/b scripts/dev.sh  # override where to look for workspaces
#
# Kept bash-3.2 compatible (macOS default) — no mapfile/readarray.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$REPO/bin/grove"
WEB="$REPO/internal/adapters/http/web"
PORT=8799

if [[ ! -x "$BIN" ]]; then
  echo "grove binary not found at $BIN — run \`make build\` first." >&2
  exit 1
fi

# A workspace passed as an argument skips discovery.
chosen="${1:-}"

if [[ -z "$chosen" ]]; then
  # Roots to search for workspaces (dirs containing grove.db). Override with
  # GROVE_WS_ROOTS (colon-separated).
  if [[ -n "${GROVE_WS_ROOTS:-}" ]]; then
    IFS=: read -ra roots <<< "$GROVE_WS_ROOTS"
  else
    roots=("$HOME/.grove" "$HOME/grove-bench" "$PWD")
  fi

  found=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && found+=("$line")
  done < <(
    for r in "${roots[@]}"; do
      [[ -d "$r" ]] || continue
      find "$r" -maxdepth 3 -name grove.db 2>/dev/null
    done | sed 's#/grove\.db$##' | sort -u
  )

  if [[ ${#found[@]} -eq 0 ]]; then
    echo "No grove workspaces found under: ${roots[*]}" >&2
    echo "Run \`grove init\` to create one, or set GROVE_WS_ROOTS=/path1:/path2." >&2
    exit 1
  fi

  echo "Available workspaces:"
  PS3=$'\nChoose a workspace # (Ctrl-C to cancel): '
  select ws in "${found[@]}"; do
    if [[ -n "${ws:-}" ]]; then
      chosen="$ws"
      break
    fi
    echo "Invalid choice — enter a number from the list."
  done
fi

if [[ ! -d "$chosen" ]]; then
  echo "Workspace not found: $chosen" >&2
  exit 1
fi

echo ""
echo "→ workspace: $chosen"
echo "→ backend on :$PORT  +  Vite on http://localhost:5173   (Ctrl-C stops both)"
echo ""

# kill 0 on exit tears down the backgrounded backend together with Vite.
trap 'kill 0' EXIT INT TERM
"$BIN" --workspace "$chosen" serve --web --port "$PORT" --no-open &
cd "$WEB" && npm run dev
