# Development

How to work on sessile. For running it, see the [README](../../README.md).

## Stack

- **Backend:** Go, Gin, gorilla/websocket, creack/pty, modernc.org/sqlite
  (pure Go, builds with `CGO_ENABLED=0`), `golang.org/x/crypto` (bcrypt +
  SSH client), `gopkg.in/yaml.v3`.
- **Frontend:** Vue 3 + TypeScript + Vite + Tailwind CSS + Pinia +
  @xterm/xterm (with the `fit`, `web-links`, `unicode11` and `webgl`
  addons).
- **Auth:** username + bcrypt-hashed password, server-side session tokens in
  an in-memory store with a sliding TTL, delivered via an `HttpOnly` cookie
  — not JWT, no session persistence.

Go 1.25+ is required.

## Quick start (development)

Run the backend and the Vite dev server in two terminals:

```bash
make dev-backend     # Go backend on :8080, state under ./sandbox/data
make dev-frontend    # Vite dev server on :5173, proxying /api and /ws to :8080
```

Then open <http://localhost:5173>. The server starts **unlocked** — the
first thing you'll see is a bootstrap form that turns whatever account you
create into the admin. From Settings, the admin can turn on local-host
sessions (today's single-tenant behavior) or self-service registration; add
SSH hosts from the Hosts page to start real sessions.

No vendored Go toolchain? `./env.sh make dev-backend` builds against a
pinned Go (and, for frontend commands, a pinned Node) fetched into `.go/` /
`.node/` instead of whatever's on `$PATH` — see [`env.sh`](../../env.sh).

## Project layout

```
backend/
  cmd/server/        entrypoint (flags, wiring, graceful shutdown)
  cmd/wsclient/      small CLI WebSocket client used by scripts/wstest.sh
  internal/
    api/             Gin router, REST handlers, error envelope, middleware
    auth/            users.yml, bcrypt, web session tokens
    serverconfig/     config.yml (display name, registration, local-host toggle)
    hosts/           per-user hosts.yml — SSH host CRUD, host-key pinning
    sshpty/          session.Backend over SSH — TOFU host-key trust, key exchange
    ws/              WebSocket endpoints + per-client read/write pumps
    session/         SessionManager, Session lifecycle, ring buffer, sandbox
    terminal/        local PTY start / resize / signal wrappers
    storage/         SQLite open + migration + queries
    config/          flag/env configuration
  web/               embeds the built SPA
frontend/
  src/
    api/             typed REST client, shared types, WS protocol codec
    composables/     useTerminal (xterm + WebSocket wiring)
    stores/          Pinia stores — sessions, auth, hosts, admin
    utils/           gesture routing, key encoding, clipboard, IME, fonts
    components/      sidebar, tab bar, terminal view, dialogs
    pages/           login, dashboard, hosts, users, terminal, settings
docs/                design specification and manual checklists
```

## Testing

```bash
make test            # go vet + go test ./...  and  vitest
```

The backend suite covers the ring buffer, the directory sandbox, ownership
scoping across sessions/hosts, SQLite storage and migration, session sizing
across several clients, SSH TOFU host-key trust and key exchange against a
real in-process (and, manually, a real system) SSH server, and full
create → attach → I/O → replay → delete flows against both a real PTY and a
real SSH session. The frontend suite covers the pure logic — the WS codec,
the REST client, key and clipboard encoding, IME handling, gesture routing —
and deliberately stops there; there is no E2E framework.

`scripts/wstest.sh` bootstraps an admin account, turns on `allowLocalHost`,
and drives a local-host session over the WebSocket protocol by hand;
`scripts/smoke-docker.sh [image]` does the same against a built container
image, plus the healthcheck and embedded-SPA checks. Both authenticate
every request with a real session cookie, matching the current auth model.

What no suite here can answer is how a gesture behaves under a thumb, so
[`docs/mobile-checklist.md`](mobile-checklist.md) carries the manual
passes: scrolling idle and under load, momentum, the backlog's ends, and
whether a full-screen program scrolls.

GitHub Actions runs `go vet`, `go test`, the frontend build and `vitest` on
every push and pull request — see
[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml).

## Wire protocol

The WebSocket carries two kinds of frames:

- **Binary frames** are raw terminal bytes, in both directions (keystrokes
  up, output and buffer replay down).
- **Text frames** are JSON control messages: `resize` (client → server), and
  `attached` / `exit` / `error` (server → client).

A separate `/ws/events` channel (also cookie-gated) pushes session list
changes — created, updated, gone — so the dashboard stays live without
polling.

## Talking to the API by hand

The REST API exists for the frontend to talk to the backend; it is not a
supported surface for anyone else, and it changes when the UI needs it to.
It is worth knowing for debugging, and `curl` is often the fastest way to
reproduce something without a browser.

Base path `/api`; all responses are JSON. Every route except `/api/health`
and `/api/auth/*` requires a valid session cookie. Errors use
`{"error":{"code":"…","message":"…"}}` with an appropriate HTTP status.

```bash
# First run: bootstrap the admin account (public, only while no user exists)
curl -s -c cookies.txt -X POST localhost:8080/api/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…"}'

# Add an SSH host
curl -s -b cookies.txt -X POST localhost:8080/api/hosts \
  -H 'Content-Type: application/json' \
  -d '{"name":"prod-db","address":"db.example.com:22","username":"deploy",
       "authMethod":"password","password":"…","targetOS":"linux","terminalType":"bash"}'

# First connection to a new host needs its key trusted before a session can
# use it — probe it, then pin the fingerprint you verified
curl -s -b cookies.txt -X POST localhost:8080/api/hosts/<id>/host-key/probe
curl -s -b cookies.txt -X POST localhost:8080/api/hosts/<id>/host-key/trust \
  -H 'Content-Type: application/json' -d '{"fingerprint":"SHA256:…","keyType":"ssh-ed25519"}'

# Create a session against that host -> 201 + session JSON
# (409 host_key_unverified/host_key_changed if the key above wasn't trusted yet)
curl -s -b cookies.txt -X POST localhost:8080/api/sessions \
  -H 'Content-Type: application/json' \
  -d '{"name":"prod-db","target":"ssh","hostId":"<id>"}'

curl -s -b cookies.txt localhost:8080/api/sessions          # list (owner-scoped)
curl -s -b cookies.txt localhost:8080/api/sessions/<id>     # get one

# Restart a stopped session: new shell/connection, same id, scrollback restored
curl -s -b cookies.txt -X POST localhost:8080/api/sessions/<id>/restart

# Delete (terminates the connection, drops its scrollback) -> 204
curl -s -b cookies.txt -X DELETE localhost:8080/api/sessions/<id>

# Files and processes on a session's target
curl -s -b cookies.txt localhost:8080/api/sessions/<id>/hostops/process-tree
curl -s -b cookies.txt 'localhost:8080/api/sessions/<id>/hostops/files?path=.'
curl -s -b cookies.txt 'localhost:8080/api/sessions/<id>/hostops/download?path=notes.txt' -o notes.txt
curl -s -b cookies.txt --data-binary @notes.txt \
  'localhost:8080/api/sessions/<id>/hostops/upload?path=notes.txt'

# Move, copy and delete. Copy and delete run in the background and report
# progress over /ws/events; the response carries the opId to follow.
curl -s -b cookies.txt -X POST localhost:8080/api/sessions/<id>/hostops/move \
  -H 'Content-Type: application/json' -d '{"src":"a.txt","dst":"b.txt"}'
curl -s -b cookies.txt -X DELETE 'localhost:8080/api/sessions/<id>/hostops/files?path=old'
```

With `allowLocalHost` on, `{"name":"…","target":"local","directory":"…","shell":"bash"}`
creates a session against the server's own shell instead. It starts in a
directory under `--workspace-dir`, and file operations through the API stay
inside it; the shell itself does not, so `cd ..` works as it would anywhere.

The terminal itself attaches over `GET /ws/sessions/:id` (WebSocket, cookie
auth); `GET /ws/events` pushes session list changes. See
[`PROJECT_PLAN.md` §6](PROJECT_PLAN.md) for the full
endpoint list, including account and admin management, host CRUD and the
SSH key exchange endpoint.

## Design documents

- [`PROJECT_PLAN.md`](PROJECT_PLAN.md) — the specification: architecture,
  wire protocol, REST surface, security model, and the milestone history.
  It is the source of truth when the two disagree.
- [`mobile-checklist.md`](mobile-checklist.md) — the manual pass for touch
  devices, which the automated suite cannot cover.
