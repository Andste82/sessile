#!/usr/bin/env bash
# upgrade-smoke.sh — the upgrade an operator does, end to end
# (PROJECT_PLAN.md §12e M35).
#
# Starts the OLD image on a fresh pair of bind mounts, creates an admin and a
# local session over the API and gives it scrollback over the WebSocket, then
# stops it the way `docker compose down` would. Starts the NEW image on the
# very same mounts and checks what must survive: the login, the session (now
# stopped), its scrollback (restart it, attach, find the marker), and — when
# the new release migrates the schema — the backup it must have written
# before doing so (§12e M32). Any ERROR or panic in either log fails it.
#
# Usage: scripts/upgrade-smoke.sh <old-image> <new-image>
#   e.g. scripts/upgrade-smoke.sh ghcr.io/andste82/sessile:0.7.0 sessile:ci
#
# Requires: docker, curl, go (to build cmd/wsclient).
set -euo pipefail

OLD="${1:?usage: $0 <old-image> <new-image>}"
NEW="${2:?usage: $0 <old-image> <new-image>}"
NAME="sessile-upgrade-$$"
PORT="${PORT:-18081}"
BASE="http://127.0.0.1:$PORT"
WORK="$(mktemp -d)"
CONFIG_DIR="$WORK/config"
WORKSPACE="$WORK/workspace"
COOKIES="$WORK/cookies"
MARKER="upgrade-marker-$$"
mkdir -p "$CONFIG_DIR" "$WORKSPACE/project-a"

cd "$(dirname "$0")/.."

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  # The container writes as root; remove its files from inside one.
  docker run --rm -v "$WORK:/w" --entrypoint sh "$NEW" -c 'rm -rf /w/config /w/workspace' \
    >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  for log in "$WORK"/*.log; do
    [ -f "$log" ] || continue
    echo "--- $(basename "$log") ---" >&2
    tail -40 "$log" >&2
  done
  if docker inspect "$NAME" >/dev/null 2>&1; then
    echo "--- running container ---" >&2
    docker logs "$NAME" 2>&1 | tail -40 >&2 || true
  fi
  exit 1
}

command -v go >/dev/null || fail "go not found on PATH (needed to build cmd/wsclient)"
echo "==> building cmd/wsclient"
( cd backend && CGO_ENABLED=0 go build -o "$WORK/wsclient" ./cmd/wsclient )

# start <image>: run it on the shared mounts and wait for its healthcheck.
start() {
  echo "==> starting $1"
  docker run -d --name "$NAME" -p "$PORT:8080" \
    -v "$CONFIG_DIR:/config" -v "$WORKSPACE:/workspace" "$1" >/dev/null
  local status=unknown
  for _ in $(seq 1 60); do
    status="$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null || echo unknown)"
    [ "$status" = healthy ] && return 0
    if ! docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | grep -q true; then
      docker logs "$NAME" >"$WORK/$2.log" 2>&1 || true
      fail "$1 exited during startup on the data from the previous image"
    fi
    sleep 1
  done
  fail "$1 never became healthy (last status: $status)"
}

# stop <label>: a graceful stop — the same SIGTERM compose sends — so the
# shells are terminated and the scrollback flushed, then keep the log.
stop() {
  docker stop -t 20 "$NAME" >/dev/null
  docker logs "$NAME" >"$WORK/$1.log" 2>&1
  docker rm "$NAME" >/dev/null
}

cookie_header() {
  local token
  token="$(awk -F'\t' '$6 == "sessile_session" { print $7 }' "$COOKIES")"
  [ -n "$token" ] || fail "no session cookie in the jar"
  echo "sessile_session=$token"
}

# schema_version <log>: what "store ready" reported. Releases before §12e M31
# do not report one, and their databases are user_version 0.
schema_version() {
  local v
  v="$(sed -n 's/.*"msg":"store ready".*"schema-version":\([0-9]*\).*/\1/p' "$1" | tail -1)"
  echo "${v:-0}"
}

check_logs() {
  if grep -qi 'panic:' "$1"; then fail "panic in $(basename "$1")"; fi
  if grep -q '"level":"ERROR"' "$1"; then fail "ERROR in $(basename "$1")"; fi
}

# --- old image -------------------------------------------------------------
start "$OLD" old

echo "==> bootstrap admin, allow local sessions"
curl -fsS -c "$COOKIES" -X POST "$BASE/api/auth/bootstrap" -H 'Content-Type: application/json' \
  -d '{"username":"upgrade","password":"upgrade-password-123"}' >/dev/null \
  || fail "bootstrap on $OLD failed"
curl -fsS -b "$COOKIES" -X PUT "$BASE/api/admin/config" -H 'Content-Type: application/json' \
  -d '{"displayName":"","allowRegistration":false,"allowLocalHost":true}' >/dev/null \
  || fail "enabling allowLocalHost on $OLD failed"

echo "==> create a session and give it scrollback"
created="$(curl -fsS -b "$COOKIES" -X POST "$BASE/api/sessions" -H 'Content-Type: application/json' \
  -d '{"name":"upgrade","target":"local","directory":"project-a","shell":"bash"}')" \
  || fail "creating a session on $OLD failed"
id="$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' <<<"$created")"
[ -n "$id" ] || fail "no session id in: $created"
# The typed line has "upgrade-$M-<pid>", only the output has the marker, so
# finding it later proves the shell's output survived, not just the echo of
# what was typed.
"$WORK/wsclient" -url "ws://127.0.0.1:$PORT/ws/sessions/$id" -cookie "$(cookie_header)" \
  -input "M=marker; echo upgrade-\$M-$$\\n" -duration 2s >"$WORK/before.out" 2>&1
grep -q "$MARKER" "$WORK/before.out" || fail "marker not in the live output on $OLD"

stop old
check_logs "$WORK/old.log"

# --- new image, same mounts ------------------------------------------------
start "$NEW" new

echo "==> log in again (web sessions are in memory, §11)"
curl -fsS -c "$COOKIES" -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"upgrade","password":"upgrade-password-123"}' >/dev/null \
  || fail "login on $NEW with the account from $OLD failed"

echo "==> the session is there, and stopped"
list="$(curl -fsS -b "$COOKIES" "$BASE/api/sessions")" || fail "listing sessions on $NEW failed"
grep -q "\"id\":\"$id\"" <<<"$list" || fail "session $id from $OLD is missing on $NEW: $list"
session="$(curl -fsS -b "$COOKIES" "$BASE/api/sessions/$id")" || fail "GET session $id failed"
grep -q '"status":"stopped"' <<<"$session" || fail "session $id is not stopped: $session"

docker logs "$NAME" >"$WORK/new.log" 2>&1
old_schema="$(schema_version "$WORK/old.log")"
new_schema="$(schema_version "$WORK/new.log")"
echo "==> schema $old_schema -> $new_schema"
if [ "$new_schema" -gt "$old_schema" ]; then
  grep -q '"msg":"backup written before migration"' "$WORK/new.log" \
    || fail "schema went $old_schema -> $new_schema without a backup being logged"
  backup="$(docker exec "$NAME" sh -c "ls -d /config/backups/*-schema-v$old_schema 2>/dev/null | tail -1")"
  [ -n "$backup" ] || fail "no backups/*-schema-v$old_schema directory"
  docker exec "$NAME" test -s "$backup/sessions.db" || fail "$backup has no sessions.db"
  docker exec "$NAME" test -s "$backup/users.yml" || fail "$backup has no users.yml"
  echo "    backup: $backup"
elif grep -q '"msg":"backup written before migration"' "$WORK/new.log"; then
  fail "a backup was written although the schema did not change ($old_schema)"
fi

echo "==> restart it and find the old scrollback in the replay"
curl -fsS -b "$COOKIES" -X POST "$BASE/api/sessions/$id/restart" | grep -q '"status":"running"' \
  || fail "restarting session $id on $NEW failed"
"$WORK/wsclient" -url "ws://127.0.0.1:$PORT/ws/sessions/$id" -cookie "$(cookie_header)" \
  -duration 2s >"$WORK/after.out" 2>&1
grep -q "$MARKER" "$WORK/after.out" || fail "scrollback from $OLD did not survive the upgrade"

stop new
check_logs "$WORK/new.log"

echo "OK: $OLD -> $NEW kept the account, the session and its scrollback"
