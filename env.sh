#!/usr/bin/env bash
# env.sh — zero-setup build environment for sessile.
#
# Keeps the Go toolchain, module/build caches, and frontend deps inside the
# repo (.go/, frontend/node_modules) so a fresh checkout can build without
# any machine-level setup, and without touching $HOME.
#
# Usage:
#   ./env.sh make build     # run one command with the env set up, then exit
#   ./env.sh                # drop into a shell with the env set up
#   . env.sh                # (bash/zsh) export the env into your current shell
#
# Sourcing only works from bash/zsh — fish users should use the exec form
# above, since `. env.sh` in fish would parse this as fish syntax.

_env_sh_prepare() {
  local root="$1"
  local godir="$root/.go"
  local goroot="$godir/toolchain/go"

  mkdir -p "$godir/path" "$godir/cache" "$godir/mod" "$godir/toolchain" || return 1

  local want_version
  want_version=$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$root/backend/go.mod" | head -1)
  [ -n "$want_version" ] || want_version="1.25.0"

  local have_version=""
  if [ -x "$goroot/bin/go" ]; then
    have_version=$("$goroot/bin/go" version 2>/dev/null | sed -n 's/.*go\([0-9.]*\).*/\1/p')
  fi

  if [ "$have_version" != "$want_version" ]; then
    echo "env.sh: installing Go $want_version into .go/toolchain ..." >&2

    local os arch
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$(uname -m)" in
      x86_64|amd64) arch=amd64 ;;
      aarch64|arm64) arch=arm64 ;;
      *)
        echo "env.sh: unsupported architecture $(uname -m)" >&2
        return 1
        ;;
    esac

    local tarball="go${want_version}.${os}-${arch}.tar.gz"
    local tmp
    tmp=$(mktemp -d) || return 1

    if ! curl -fsSL "https://go.dev/dl/${tarball}" -o "$tmp/${tarball}"; then
      echo "env.sh: failed to download https://go.dev/dl/${tarball}" >&2
      rm -rf "$tmp"
      return 1
    fi

    rm -rf "$goroot"
    if ! tar -C "$godir/toolchain" -xzf "$tmp/${tarball}"; then
      echo "env.sh: failed to extract $tarball" >&2
      rm -rf "$tmp"
      return 1
    fi
    rm -rf "$tmp"
  fi

  export GOROOT="$goroot"
  export GOPATH="$godir/path"
  export GOCACHE="$godir/cache"
  export GOMODCACHE="$godir/mod"
  export CGO_ENABLED=0
  export PATH="$goroot/bin:$PATH"

  if [ ! -d "$root/frontend/node_modules" ]; then
    echo "env.sh: installing frontend dependencies ..." >&2
    if [ -f "$root/frontend/package-lock.json" ]; then
      (cd "$root/frontend" && npm ci) || return 1
    else
      (cd "$root/frontend" && npm install) || return 1
    fi
  fi

  return 0
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  # Executed: ./env.sh [cmd...]
  set -euo pipefail
  _ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  _env_sh_prepare "$_ROOT"
  cd "$_ROOT"
  if [ "$#" -eq 0 ]; then
    exec "${SHELL:-/bin/bash}"
  else
    exec "$@"
  fi
else
  # Sourced: . env.sh (bash/zsh only)
  _ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if _env_sh_prepare "$_ROOT"; then
    cd "$_ROOT"
    echo "env.sh: ready — $("$GOROOT/bin/go" version)"
  else
    echo "env.sh: setup failed" >&2
  fi
  unset -f _env_sh_prepare
  unset _ROOT
fi
