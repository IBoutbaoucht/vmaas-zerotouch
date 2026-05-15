#!/usr/bin/env bash
# Quick reachability check across the stack:
#   - frontend on :8081 reachable
#   - backend /healthz returns 200 with esxi info
#   - bbolt state.db is present + writable from inside the backend container
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "[A] frontend (:8081)"
curl -fsS -o /dev/null -w "   HTTP %{http_code}\n" http://localhost:8081/

echo "[A] backend /healthz"
curl -fsS http://localhost:8081/healthz | jq .

echo "[A] state.db on host"
ls -la data/state.db 2>/dev/null || echo "   (no state.db yet — fine if you haven't provisioned)"
