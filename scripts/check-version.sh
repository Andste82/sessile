#!/usr/bin/env bash
# check-version.sh — guard against version drift before a release goes out.
#
# sessile has exactly one declared version: frontend/package.json. Everything
# else (config.go, Makefile, Dockerfile, docker-compose.yml) carries a "dev"
# placeholder and gets the real value injected from the git tag at build time.
#
# This checks both halves of that contract:
#   1. the tag being released matches the declared version, and
#   2. nobody re-hardcoded a release version into a placeholder.
#
# Usage: scripts/check-version.sh 0.2.0     # verify the repo is ready for v0.2.0
#        scripts/check-version.sh           # just check placeholders + print
#
# Requires: bash, grep. No jq (not guaranteed on every runner).
set -euo pipefail

cd "$(dirname "$0")/.."

WANT="${1:-}"
fail=0

err() {
  echo "::error::$*" 2>/dev/null || true
  echo "FAIL: $*" >&2
  fail=1
}

# --- the one declared version ----------------------------------------------
DECLARED="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  frontend/package.json | head -1)"
if [ -z "$DECLARED" ]; then
  err "no version found in frontend/package.json"
  exit 1
fi
echo "declared version (frontend/package.json): $DECLARED"

if [ -n "$WANT" ]; then
  echo "requested release version (git tag):      $WANT"
  if [ "$DECLARED" != "$WANT" ]; then
    err "version drift: tag says '$WANT' but frontend/package.json says '$DECLARED'. Bump package.json and re-tag."
  fi
fi

# --- placeholders must stay placeholders ------------------------------------
# Each entry: <file>:<regex the line must match>:<description>
check_placeholder() {
  local file="$1" pattern="$2" desc="$3"
  if ! grep -Eq "$pattern" "$file"; then
    err "$desc — expected a 'dev' placeholder in $file, found a hardcoded version. The release derives the version from the git tag; hardcoding it here makes it drift."
  fi
}

check_placeholder backend/internal/config/config.go \
  '^var Version = "dev"$' "config.go Version"
check_placeholder Makefile \
  '^VERSION[[:space:]]*\?=[[:space:]]*dev$' "Makefile VERSION default"
check_placeholder Dockerfile \
  '^ARG VERSION=dev$' "Dockerfile VERSION arg"
check_placeholder docker-compose.yml \
  '^[[:space:]]*image:[[:space:]]*sessile:\$\{VERSION:-dev\}$' "compose image tag"

if [ "$fail" -ne 0 ]; then
  echo "version check FAILED" >&2
  exit 1
fi

echo "OK: version check passed"
