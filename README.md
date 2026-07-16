# sessile

A lightweight, browser-based **persistent terminal session manager** — think
*tmux + the VS Code integrated terminal, in the browser*. PTYs live in the Go
backend and survive browser disconnects; the browser is a dumb view that
replays raw terminal bytes via xterm.js.

See [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md) for the full specification.

## Features

- Persistent PTY sessions that survive browser disconnects and page refreshes.
- Scrollback restoration on reconnect via a per-session raw-byte ring buffer
  (no server-side terminal emulation).
- Multiple clients attached to one session, mirrored live; live client counts.
- Auto-reconnect with exponential backoff; responsive UI with a session tab bar
  and mobile bottom navigation.
- Single static binary (embedded SPA, `CGO_ENABLED=0`) or a small container.

## Stack

- **Backend:** Go 1.22+, Gin, gorilla/websocket, creack/pty, modernc.org/sqlite
  (pure Go, `CGO_ENABLED=0`).
- **Frontend:** Vue 3 + TypeScript + Vite + Tailwind v4 + Pinia + @xterm/xterm.

## Quick start (dev)

```bash
make dev-backend     # Go backend on :8080, sandbox rooted at ./sandbox
make dev-frontend    # Vite dev server on :5173, proxies /api and /ws to :8080
```

Open http://localhost:5173.

## Build & run (production single binary)

```bash
make build           # builds the SPA, embeds it, produces ./bin/sessile
./bin/sessile --root=/path/to/workspace
```

Open http://localhost:8080.

## Docker

```bash
# Build and run with compose (mounts ./workspace, persists metadata in a volume)
docker compose up --build

# …or build and run the image directly
make docker
docker run -p 8080:8080 \
  -v "$PWD/workspace:/workspace" -v sessile-config:/config \
  sessile:0.1.0
```

Open http://localhost:8080. The image is multi-stage (Node builds the SPA →
Go builds a static binary → `alpine` runtime with `bash` for shells) and ships
a `/api/health` `HEALTHCHECK`. Volumes: `/workspace` (session root) and
`/config` (SQLite metadata).

## Configuration

| Flag | Env | Default |
|---|---|---|
| `--root` | `TSM_ROOT` | *(required)* — sandbox root for all sessions |
| `--addr` | `TSM_ADDR` | `:8080` |
| `--db` | `TSM_DB` | `<root>/.tsm/sessions.db` |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` (allowlist) |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` |
| `--log-level` | `TSM_LOG_LEVEL` | `info` |
| `--dev` | `TSM_DEV` | `false` — relaxes the WS origin check for Vite |

## REST API walkthrough

With the backend running on `:8080` and a `project-a` directory under the
sandbox root:

```bash
# Health
curl -s localhost:8080/api/health
# {"status":"ok"}

# Config (root, installed shells from the allowlist, version)
curl -s localhost:8080/api/config

# Directories one level under the root
curl -s localhost:8080/api/directories
# {"directories":["project-a"]}

# Create a session -> 201 + session JSON
curl -s -X POST localhost:8080/api/sessions \
  -H 'Content-Type: application/json' \
  -d '{"name":"Backend","directory":"project-a","shell":"bash"}'

# List / get
curl -s localhost:8080/api/sessions
curl -s localhost:8080/api/sessions/<id>

# Rename
curl -s -X PATCH localhost:8080/api/sessions/<id> \
  -H 'Content-Type: application/json' -d '{"name":"API"}'

# Delete (kills the shell) -> 204
curl -s -X DELETE localhost:8080/api/sessions/<id>
```

Errors use `{"error":{"code":"…","message":"…"}}` with an appropriate status
(400 validation, 404 missing, 409 conflict, 500 internal).

The WebSocket protocol is exercised by `scripts/wstest.sh` (create → attach →
input → reconnect → replay).

## Caveats

- **Live sessions do not survive a backend restart.** The PTY/shell processes
  are children of the backend; on restart any session still marked `running` in
  SQLite is reconciled to `stopped`. This is by design for v0.x.
- No authentication in v0.1 — deploy behind a reverse proxy. Auth arrives in
  v0.4.

## License

MIT — see [`LICENSE`](LICENSE).
