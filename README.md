# sessile

A lightweight, browser-based **persistent terminal session manager** — think
*tmux + the VS Code integrated terminal, in the browser*. PTYs live in the Go
backend and survive browser disconnects; the browser is a dumb view that
replays raw terminal bytes via xterm.js.

See [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md) for the full specification.

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

## Caveats

- **Live sessions do not survive a backend restart.** The PTY/shell processes
  are children of the backend; on restart any session still marked `running` in
  SQLite is reconciled to `stopped`. This is by design for v0.x.
- No authentication in v0.1 — deploy behind a reverse proxy. Auth arrives in
  v0.4.

## License

MIT — see [`LICENSE`](LICENSE).
