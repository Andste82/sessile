#!/usr/bin/env bash
set -eo pipefail

# Optional first argument "rebuild" forces a fresh image build and replaces any
# existing container (even if one already exists).
REBUILD=0
if [[ "${1:-}" == "rebuild" ]]; then
  REBUILD=1
fi

# Define the container image name
IMAGE_NAME="sessile-devcontainer"

# Resolve this script's directory so it works regardless of the caller's cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/.env"
CLAUDE_CONFIG_DIR="$SCRIPT_DIR/.claude-config"
mkdir -p "$CLAUDE_CONFIG_DIR"

# Derive container name from parent folder, e.g. sessile-<project-folder>
PROJECT_NAME="$(basename "$(dirname "$SCRIPT_DIR")")"
CONTAINER_NAME="sessile-${PROJECT_NAME}"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

env_args=()
if [[ -f "$ENV_FILE" ]]; then
  env_args=(--env-file "$ENV_FILE")
else
  echo "warning: $ENV_FILE not found — copy .devcontainer/.env.example to .devcontainer/.env to load secrets." >&2
fi

# Build image if missing (or unconditionally when rebuild was requested).
if [[ "$REBUILD" == "1" ]]; then
  echo "Rebuilding image '$IMAGE_NAME' from $SCRIPT_DIR"
  docker build -t "$IMAGE_NAME" "$SCRIPT_DIR"
elif ! docker image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
  echo "Image '$IMAGE_NAME' not found — building from $SCRIPT_DIR"
  docker build -t "$IMAGE_NAME" "$SCRIPT_DIR"
fi

# On rebuild, stop and remove any existing container so a fresh one is started
# from the newly built image.
if [[ "$REBUILD" == "1" ]]; then
  if docker ps -a --filter "name=^/${CONTAINER_NAME}$" --format '{{.ID}}' | grep -q .; then
    echo "Stopping and removing existing container '$CONTAINER_NAME'..."
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  fi
fi

# If the container exists and is already running, attach to it.
if docker ps --filter "name=^/${CONTAINER_NAME}$" --filter "status=running" --format '{{.ID}}' | grep -q .; then
  echo "Attaching to running container '$CONTAINER_NAME'..."
  docker exec -it "$CONTAINER_NAME" bash
  exit 0
fi

# If the container exists but is stopped, start it and attach.
if docker ps -a --filter "name=^/${CONTAINER_NAME}$" --format '{{.ID}}' | grep -q .; then
  echo "Starting existing container '$CONTAINER_NAME'..."
  docker start "$CONTAINER_NAME"
  echo "Attaching to container '$CONTAINER_NAME'..."
  docker exec -it "$CONTAINER_NAME" bash
  exit 0
fi

# Otherwise run a new container with an explicit shell entrypoint.
echo "Running new container '$CONTAINER_NAME'..."
docker run -it --rm --name "$CONTAINER_NAME" \
  "${env_args[@]}" \
  -e CLAUDE_CONFIG_DIR=/root/.claude \
  -v "$PROJECT_ROOT":/workspace \
  -v "$CLAUDE_CONFIG_DIR":/root/.claude \
  --workdir /workspace \
  "$IMAGE_NAME" \
  bash

