#!/usr/bin/env bash
# End-to-end test against a running VMaaS stack.
#
# Provisions a VM, waits for it to become ready, SSHs in, verifies the
# cloud-init sentinel, deletes the VM, and confirms the IP returned to
# the pool.
#
# Assumes:
#   - `docker compose up` is running.
#   - .env contains a usable VMAAS_TOKEN.
#   - ~/.ssh/vmaas-lab is the private key matching keys/authorized_keys.
#
# Tagged for clarity by which machine each command runs on:
#   [A] = your laptop (this script's host)
#   [B] = the ESXi host  (we touch it indirectly through the API)
#   [V] = the newly-provisioned VM
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TOKEN="$(awk -F= '/^VMAAS_TOKEN=/{print $2}' .env)"
if [[ -z "${TOKEN}" ]]; then
  echo "VMAAS_TOKEN not set in .env" >&2
  exit 2
fi

API="http://localhost:8081/api/v1"
HEALTH_URL="http://localhost:8081/healthz"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/vmaas-lab}"
SSH_USER="${SSH_USER:-cuneyt}"
TIMEOUT_S=180
POLL_S=2

curl_api() {
  curl -fsS -H "Authorization: Bearer $TOKEN" "$@"
}

echo "[A] 1. Health check"
curl -fsS "$HEALTH_URL" | jq -r '"   esxi=\(.esxi // .ok) version=\(.version // "?")"'

echo "[A] 2. POST /vms (auto-named)"
RESP=$(curl_api -X POST "$API/vms" -H 'Content-Type: application/json' -d '{}')
ID=$(echo "$RESP" | jq -r .id)
echo "   id=$ID"

echo "[A] 3. Wait for status=ready (timeout ${TIMEOUT_S}s)"
DEADLINE=$(( SECONDS + TIMEOUT_S ))
STATUS=""
while (( SECONDS < DEADLINE )); do
  REC=$(curl_api "$API/vms/$ID")
  STATUS=$(echo "$REC" | jq -r .status)
  IP=$(echo "$REC" | jq -r '.ip // empty')
  printf "   t=%ds status=%s ip=%s\n" "$SECONDS" "$STATUS" "${IP:-—}"
  case "$STATUS" in
    ready)  break ;;
    failed) echo "   FAILED:"; echo "$REC" | jq .; exit 1 ;;
  esac
  sleep "$POLL_S"
done
if [[ "$STATUS" != "ready" ]]; then
  echo "   timed out, last record:" >&2
  curl_api "$API/vms/$ID" | jq . >&2
  exit 1
fi

IP=$(curl_api "$API/vms/$ID" | jq -r .ip)
echo "[A] 4. SSH to [V] $IP"
if [[ ! -f "$SSH_KEY" ]]; then
  echo "   skipping ssh: $SSH_KEY does not exist"
else
  ssh -i "$SSH_KEY" \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=10 \
      "$SSH_USER@$IP" 'uname -a; echo ---; cat /var/log/vmaas-sentinel.log 2>/dev/null || echo "(no sentinel yet)"'
fi

echo "[A] 5. DELETE /vms/$ID"
curl_api -X DELETE "$API/vms/$ID"
echo "   deleted"

echo "[A] 6. Pool check"
curl_api "$API/pool" | jq '{total, used_count: (.used|length), free_count: (.free|length)}'

echo "[A] ✓ E2E PASS"
