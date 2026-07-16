#!/usr/bin/env bash
# wstest.sh — M1 manual walkthrough (PROJECT_PLAN.md §12 M1 acceptance).
#
# Boots the backend against a temp sandbox, creates a session over REST,
# connects a WS client to send `echo`, disconnects, then reconnects a second
# client and confirms the ring-buffer replay contains the earlier output.
#
# Requires: go, curl, jq (optional; falls back to grep).
set -euo pipefail

cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "go not found on PATH"; exit 1; }

ADDR="127.0.0.1:8099"
ROOT="$(mktemp -d)"
DB="$(mktemp -u).db"
SHELL_NAME="sh"
mkdir -p "$ROOT/project-a"

echo "== building server + wsclient =="
( cd backend && go build -o /tmp/sessile-wstest-server ./cmd/server )
( cd backend && go build -o /tmp/sessile-wsclient ./cmd/wsclient )

echo "== starting server (root=$ROOT) =="
/tmp/sessile-wstest-server --root="$ROOT" --db="$DB" --addr="$ADDR" \
  --shells="$SHELL_NAME,bash" --dev >/tmp/sessile-wstest.log 2>&1 &
SRV=$!
cleanup() { kill "$SRV" 2>/dev/null || true; rm -rf "$ROOT" "$DB"; }
trap cleanup EXIT

# Wait for health.
for _ in $(seq 1 30); do
  curl -sf "http://$ADDR/api/health" >/dev/null && break
  sleep 0.2
done

echo "== POST /api/sessions =="
RESP="$(curl -sf -X POST "http://$ADDR/api/sessions" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"wstest\",\"directory\":\"project-a\",\"shell\":\"$SHELL_NAME\"}")"
echo "$RESP"
if command -v jq >/dev/null; then
  ID="$(echo "$RESP" | jq -r .id)"
else
  ID="$(echo "$RESP" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
fi
[ -n "$ID" ] || { echo "failed to create session"; exit 1; }
echo "session id: $ID"

WS="ws://$ADDR/ws/sessions/$ID"

echo "== client 1: send 'echo hello-from-wstest' =="
/tmp/sessile-wsclient -url "$WS" -input 'echo hello-from-wstest\n' -duration 2s | tee /tmp/wstest-c1.out

echo "== client 2: reconnect, expect replay to contain earlier output =="
/tmp/sessile-wsclient -url "$WS" -duration 2s | tee /tmp/wstest-c2.out

if grep -q "hello-from-wstest" /tmp/wstest-c2.out; then
  echo "PASS: replay contained earlier output"
else
  echo "FAIL: replay did not contain earlier output"
  exit 1
fi

echo "== DELETE /api/sessions/$ID =="
curl -sf -o /dev/null -w "delete status: %{http_code}\n" -X DELETE "http://$ADDR/api/sessions/$ID"
echo "== done =="
