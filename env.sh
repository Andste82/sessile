#!/usr/bin/env bash
# env.sh — zero-setup build environment for sessile.
#
# Keeps the Go toolchain, Node toolchain, module/build caches, and frontend
# deps inside the repo (.go/, .node/, frontend/node_modules) so a fresh
# checkout can build without any machine-level setup, and without touching
# $HOME.
#
# Usage:
#   ./env.sh make build     # run one command with the env set up, then exit
#   ./env.sh                # drop into a shell with the env set up
#   . env.sh                # (bash/zsh) export the env into your current shell
#   ./env.sh --fresh ...    # wipe .go/, .node/, frontend/node_modules first
#
# Sourcing only works from bash/zsh — fish users should use the exec form
# above, since `. env.sh` in fish would parse this as fish syntax.

# Node version pinned here since frontend/package.json has no "engines"
# field to read it from (@tailwindcss/oxide needs >= 20; npm silently skips
# its native binding otherwise).
_ENV_SH_NODE_VERSION="22.11.0"

_env_sh_clean() {
  local root="$1"
  echo "env.sh: --fresh — removing $root/.go, $root/.node and $root/frontend/node_modules ..." >&2
  rm -rf "$root/.go" "$root/.node" "$root/frontend/node_modules"
}

_env_sh_platform() {
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
  echo "$os $arch"
}

_env_sh_prepare() {
  local root="$1"
  local os arch
  read -r os arch < <(_env_sh_platform) || return 1

  local godir="$root/.go"
  local goroot="$godir/toolchain/go"

  mkdir -p "$godir/path" "$godir/cache" "$godir/mod" "$godir/toolchain" || return 1

  local want_go_version
  want_go_version=$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$root/backend/go.mod" | head -1)
  [ -n "$want_go_version" ] || want_go_version="1.25.0"

  local have_go_version=""
  if [ -x "$goroot/bin/go" ]; then
    have_go_version=$("$goroot/bin/go" version 2>/dev/null | sed -n 's/.*go\([0-9.]*\).*/\1/p')
  fi

  if [ "$have_go_version" != "$want_go_version" ]; then
    echo "env.sh: installing Go $want_go_version into .go/toolchain ..." >&2

    local tarball="go${want_go_version}.${os}-${arch}.tar.gz"
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

  local nodedir="$root/.node"
  local node_arch="$arch"
  [ "$node_arch" = "amd64" ] && node_arch="x64"
  local nodehome="$nodedir/toolchain/node-v${_ENV_SH_NODE_VERSION}-${os}-${node_arch}"

  mkdir -p "$nodedir/toolchain" "$nodedir/cache" || return 1

  local have_node_version=""
  if [ -x "$nodehome/bin/node" ]; then
    have_node_version=$("$nodehome/bin/node" --version 2>/dev/null | sed -n 's/^v//p')
  fi

  if [ "$have_node_version" != "$_ENV_SH_NODE_VERSION" ]; then
    echo "env.sh: installing Node $_ENV_SH_NODE_VERSION into .node/toolchain ..." >&2

    local node_tarball="node-v${_ENV_SH_NODE_VERSION}-${os}-${node_arch}.tar.gz"
    local tmp
    tmp=$(mktemp -d) || return 1

    if ! curl -fsSL "https://nodejs.org/dist/v${_ENV_SH_NODE_VERSION}/${node_tarball}" -o "$tmp/${node_tarball}"; then
      echo "env.sh: failed to download https://nodejs.org/dist/v${_ENV_SH_NODE_VERSION}/${node_tarball}" >&2
      rm -rf "$tmp"
      return 1
    fi

    rm -rf "$nodehome"
    if ! tar -C "$nodedir/toolchain" -xzf "$tmp/${node_tarball}"; then
      echo "env.sh: failed to extract $node_tarball" >&2
      rm -rf "$tmp"
      return 1
    fi
    rm -rf "$tmp"
  fi

  export PATH="$nodehome/bin:$PATH"
  export npm_config_cache="$nodedir/cache"

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
  # Executed: ./env.sh [--fresh] [cmd...]
  set -euo pipefail
  _ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [ "${1:-}" = "--fresh" ]; then
    shift
    _env_sh_clean "$_ROOT"
  fi
  _env_sh_prepare "$_ROOT"
  cd "$_ROOT"
  if [ "$#" -eq 0 ]; then
    exec "${SHELL:-/bin/bash}"
  else
    exec "$@"
  fi
else
  # Sourced: . env.sh [--fresh] (bash/zsh only)
  _ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [ "${1:-}" = "--fresh" ]; then
    shift
    _env_sh_clean "$_ROOT"
  fi
  if _env_sh_prepare "$_ROOT"; then
    cd "$_ROOT"
    echo "env.sh: ready — $("$GOROOT/bin/go" version), $(node --version)"
  else
    echo "env.sh: setup failed" >&2
  fi
  unset -f _env_sh_prepare _env_sh_clean _env_sh_platform
  unset _ROOT
fi
