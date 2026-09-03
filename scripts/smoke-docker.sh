#!/usr/bin/env bash
# smoke-docker.sh — verify a built image actually serves sessile, updated
# for the M8-M21 multi-user auth model: every REST route except /api/health
# and /api/auth/* now requires a session cookie, and session creation needs
# a target discriminator (local-host sessions must also be turned on).
#
# Starts the image against temp config/workspace volumes, waits for the
# container to report healthy, bootstraps the admin account, turns on
# allowLocalHost, then exercises the SPA and the REST surface end to end:
# create a session, confirm it is listed and running, delete it again.
#
# Usage: scripts/smoke-docker.sh [image]   (default: sessile:dev)
#
# Requires: docker, curl.
set -euo pipefail

IMAGE="${1:-sessile:dev}"
NAME="sessile-smoke-$$"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:$PORT"
CONFIG_DIR="$(mktemp -d)"
WORKSPACE="$(mktemp -d)"
COOKIES="$(mktemp)"
mkdir -p "$WORKSPACE/project-a"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "$CONFIG_DIR" "$WORKSPACE" "$COOKIES"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  echo "--- container logs ---" >&2
  docker logs "$NAME" 2>&1 | tail -50 >&2 || true
  exit 1
}

echo "==> starting $IMAGE as $NAME on :$PORT"
docker run -d --name "$NAME" -p "$PORT:8080" \
  -v "$CONFIG_DIR:/config" -v "$WORKSPACE:/workspace" "$IMAGE" >/dev/null

# The image ships a HEALTHCHECK; use it as the readiness signal so this test
# also proves the healthcheck itself works.
echo "==> waiting for healthy"
status=unknown
for _ in $(seq 1 60); do
  status="$(docker inspect -f '{{.State.Health.Status}}' "$NAME" 2>/dev/null || echo unknown)"
  if [ "$status" = "healthy" ]; then
    break
  fi
  if [ "$status" = "unhealthy" ]; then
    fail "container reported unhealthy"
  fi
  if ! docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | grep -q true; then
    fail "container exited during startup"
  fi
  sleep 1
done
if [ "$status" != "healthy" ]; then
  fail "container never became healthy (last status: $status)"
fi

echo "==> GET /api/health"
curl -fsS "$BASE/api/health" | grep -q '"status":"ok"' || fail "health payload unexpected"

echo "==> GET / (embedded SPA)"
# Proves the frontend really got embedded, not just the committed placeholder.
curl -fsS "$BASE/" | grep -qi '<div id="app"' || fail "SPA index.html not served"

echo "==> POST /api/auth/bootstrap (server starts unlocked, §10)"
bootstrap="$(curl -fsS -c "$COOKIES" -X POST "$BASE/api/auth/bootstrap" \
  -H 'Content-Type: application/json' \
  -d '{"username":"smoke","password":"smoke-password-123"}')"
grep -q '"isAdmin":true' <<<"$bootstrap" || fail "bootstrap did not return an admin user: $bootstrap"

echo "==> PUT /api/admin/config (allowLocalHost=true, §9)"
curl -fsS -b "$COOKIES" -X PUT "$BASE/api/admin/config" \
  -H 'Content-Type: application/json' \
  -d '{"displayName":"","allowRegistration":false,"allowLocalHost":true}' >/dev/null \
  || fail "enabling allowLocalHost failed"

echo "==> GET /api/config"
config="$(curl -fsS -b "$COOKIES" "$BASE/api/config")"
grep -q '"bash"' <<<"$config" || fail "bash missing from shell allowlist: $config"
grep -q '"allowLocalHost":true' <<<"$config" || fail "allowLocalHost did not take effect: $config"

echo "==> POST /api/sessions (target: local, spawns a real pty)"
created="$(curl -fsS -b "$COOKIES" -X POST "$BASE/api/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"name":"smoke","target":"local","directory":"project-a","shell":"bash"}')"
id="$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' <<<"$created")"
[ -n "$id" ] || fail "no session id in response: $created"
grep -q '"status":"running"' <<<"$created" || fail "session not running: $created"

echo "==> GET /api/sessions"
curl -fsS -b "$COOKIES" "$BASE/api/sessions" | grep -q "$id" || fail "created session not listed"

echo "==> DELETE /api/sessions/$id"
curl -fsS -b "$COOKIES" -o /dev/null -X DELETE "$BASE/api/sessions/$id" || fail "delete failed"

echo "==> GET /api/auth/me without a cookie must be rejected (§10/§11)"
unauth_status="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/auth/me")"
[ "$unauth_status" = "401" ] || fail "unauthenticated /api/auth/me returned $unauth_status, want 401"

echo "==> checking for panics in logs"
if docker logs "$NAME" 2>&1 | grep -qi 'panic:'; then
  fail "panic in container logs"
fi

echo "OK: $IMAGE passed the smoke test"
