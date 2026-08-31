#!/usr/bin/env bash
# wstest.sh — M1 manual walkthrough (PROJECT_PLAN.md §12 M1 acceptance),
# updated for the M8-M21 multi-user auth model: every REST and WS route
# except /api/health and /api/auth/* now requires a session cookie.
#
# Bootstraps an admin account, turns on allowLocalHost, boots the backend
# against a temp workspace, creates a local-host session over REST, connects
# a WS client to send `echo`, disconnects, then reconnects a second client
# and confirms the ring-buffer replay contains the earlier output.
#
# Requires: go, curl, jq (optional; falls back to grep).
set -euo pipefail

cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "go not found on PATH"; exit 1; }

ADDR="127.0.0.1:8099"
DATA_DIR="$(mktemp -d)"
WORKSPACE_DIR="$(mktemp -d)"
SHELL_NAME="sh"
COOKIES="$(mktemp)"
mkdir -p "$WORKSPACE_DIR/project-a"

echo "== building server + wsclient =="
( cd backend && go build -o /tmp/sessile-wstest-server ./cmd/server )
( cd backend && go build -o /tmp/sessile-wsclient ./cmd/wsclient )

echo "== starting server (data-dir=$DATA_DIR, workspace-dir=$WORKSPACE_DIR) =="
/tmp/sessile-wstest-server --data-dir="$DATA_DIR" --workspace-dir="$WORKSPACE_DIR" --addr="$ADDR" \
  --shells="$SHELL_NAME,bash" --dev >/tmp/sessile-wstest.log 2>&1 &
SRV=$!
cleanup() { kill "$SRV" 2>/dev/null || true; rm -rf "$DATA_DIR" "$WORKSPACE_DIR" "$COOKIES"; }
trap cleanup EXIT

# Wait for health.
for _ in $(seq 1 30); do
  curl -sf "http://$ADDR/api/health" >/dev/null && break
  sleep 0.2
done

json_field() {
  # $1: JSON on stdin's field, via jq if available, else a crude grep.
  if command -v jq >/dev/null; then
    jq -r ".$1"
  else
    sed -n "s/.*\"$1\":\"\?\([^\",}]*\)\"\?.*/\1/p"
  fi
}

echo "== POST /api/auth/bootstrap (server starts unlocked, §10) =="
BOOTSTRAP="$(curl -sf -c "$COOKIES" -X POST "http://$ADDR/api/auth/bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"username":"wstest","password":"wstest-password-123"}')"
echo "$BOOTSTRAP"

echo "== PUT /api/admin/config (allowLocalHost=true, §9) =="
curl -sf -b "$COOKIES" -X PUT "http://$ADDR/api/admin/config" \
  -H 'Content-Type: application/json' \
  -d '{"displayName":"","allowRegistration":false,"allowLocalHost":true}' >/dev/null

echo "== POST /api/sessions (target: local, §6) =="
RESP="$(curl -sf -b "$COOKIES" -X POST "http://$ADDR/api/sessions" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"wstest\",\"target\":\"local\",\"directory\":\"project-a\",\"shell\":\"$SHELL_NAME\"}")"
echo "$RESP"
ID="$(echo "$RESP" | json_field id)"
[ -n "$ID" ] || { echo "failed to create session"; exit 1; }
echo "session id: $ID"

# The session cookie the WS handshake needs — every /ws/* route requires it
# too (router.go's requireAuth on both /ws/sessions/:id and /ws/events).
# curl's cookie jar is Netscape format: tab-separated fields, name then value
# are the last two.
TOKEN="$(awk -F'\t' '$6 == "sessile_session" { print $7 }' "$COOKIES")"
[ -n "$TOKEN" ] || { echo "failed to extract the session cookie from $COOKIES"; exit 1; }
COOKIE_HEADER="sessile_session=$TOKEN"

WS="ws://$ADDR/ws/sessions/$ID"

echo "== client 1: send 'echo hello-from-wstest' =="
/tmp/sessile-wsclient -url "$WS" -cookie "$COOKIE_HEADER" -input 'echo hello-from-wstest\n' -duration 2s | tee /tmp/wstest-c1.out

echo "== client 2: reconnect, expect replay to contain earlier output =="
/tmp/sessile-wsclient -url "$WS" -cookie "$COOKIE_HEADER" -duration 2s | tee /tmp/wstest-c2.out

if grep -q "hello-from-wstest" /tmp/wstest-c2.out; then
  echo "PASS: replay contained earlier output"
else
  echo "FAIL: replay did not contain earlier output"
  exit 1
fi

echo "== DELETE /api/sessions/$ID =="
curl -sf -b "$COOKIES" -o /dev/null -w "delete status: %{http_code}\n" -X DELETE "http://$ADDR/api/sessions/$ID"
echo "== done =="
