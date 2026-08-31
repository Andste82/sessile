![sessile logo](./assets/sessile_logo.png)

# sessile

A lightweight, browser-based, **multi-user** terminal session manager for
SSH-reachable hosts — think *tmux + the VS Code integrated terminal, in the
browser, per user, per host*.

Each user logs in, configures their own SSH hosts (name, group, credentials,
shell/terminal type), and opens persistent terminal sessions against them.
Sessions live in the Go backend and keep running even when every browser tab
is closed. The browser is a thin view: it streams raw terminal bytes to and
from the backend and renders them with xterm.js. Reopen the page and your
shell — colors, cursor, scrollback and all — is restored exactly as you left
it.

See [`docs/PROJECT_PLAN.md`](docs/PROJECT_PLAN.md) for the full design
specification.

## Features

- **Multi-user accounts.** The server starts unlocked; the first person to
  log in becomes the admin. Self-service registration is an admin-controlled
  toggle, off by default.
- **Per-user SSH hosts.** Each user configures their own hosts — name,
  group, address, credentials, target OS, shell/terminal type — with no
  overlap between users' credentials.
- **SSH host-key trust-on-first-use.** The first connection to a host, or
  any later connection where the presented key no longer matches, prompts
  the real fingerprint and requires explicit confirmation before it
  connects — never a silent accept.
- **Exchange SSH keys.** Set up passwordless login for a host in one step:
  enter credentials once, the server generates a keypair, installs the
  public half remotely, and switches the host to it — the password you
  typed is used for exactly that one connection and is never stored.
- **Optional local-host fallback.** An admin can enable sessions against the
  server's own shell (today's single-tenant behavior), sharing one workspace
  root across every user permitted to use it.
- **Persistent sessions.** Once connected, PTYs are owned by the backend and
  survive browser disconnects, refreshes and closed tabs.
- **Scrollback restoration.** Each session keeps a ring buffer of its raw
  output bytes; on (re)connect the buffer is replayed and xterm.js
  re-renders the ANSI stream — no server-side terminal emulation.
- **Restart after a backend restart.** A stopped session can be brought back
  under the same id with one click: same target, its scrollback replayed,
  and (for local sessions) arrow-up still walking the commands typed in that
  session.
- **Multi-client.** Several browsers can attach to the same session and see
  it mirrored live, with per-session client counts. The session is sized to
  the smallest window attached to it, so its output fits every one of them.
- **Resilient UI.** Automatic reconnect with exponential backoff, a session
  tab bar, and a responsive layout that adapts from desktop to mobile.
- **Usable by touch.** A one-finger drag scrolls the backlog and keeps
  coasting after a flick, with an on-screen bar supplying Ctrl/Alt/Shift and
  the keys a phone keyboard has none of.
- **GPU rendering.** The terminal draws through WebGL, falling back to
  xterm's DOM renderer where no WebGL context can be had.
- **Single binary or container.** The frontend is embedded into a static,
  CGO-free Go binary; a small multi-stage container image is also provided.

## How it works

```
Browser (xterm.js) ⇄ WebSocket ⇄ Go backend ⇄ session.Backend ⇄ local shell (PTY)
     (session cookie)            (auth-gated)  ⇄ session.Backend ⇄ remote shell (SSH)
                     REST (JSON) ⇅
                    SQLite (session metadata) + config.yml/users.yml/hosts.yml
```

- Every request — REST and WebSocket alike — carries the browser's session
  cookie and is scoped to the authenticated user; a client-supplied user or
  host id is never trusted.
- `session.Backend` is the abstraction that lets one `Manager` drive both a
  **local PTY** and a **remote SSH session** through identical
  read/broadcast/scrollback/restart code — nothing above it knows or cares
  which kind of shell it's talking to.
- Each session runs **one goroutine** that reads its backend, appends the
  bytes to a ring buffer, and broadcasts them to all attached clients. Each
  WebSocket connection has exactly one writer goroutine, and a slow client
  is dropped rather than allowed to stall the others.
- **SQLite stores session metadata only** (id, name, owner, target, status,
  timestamps). Accounts, server settings and per-user SSH hosts live in
  hand-editable YAML instead (see [Configuration](#configuration)).
- **One PTY, one size.** Every attached client reports the geometry it can
  display, and the backend is sized to the smallest rows and the smallest
  cols among them, per axis.
- Because shells are owned by the backend process, **live sessions do not
  survive a backend restart.** On startup any session still marked `running`
  is reconciled to `stopped`. A stopped session can be **restarted**: it
  gets a new shell/connection under the same id, with its scrollback
  restored — for local sessions, command history too. What does not come
  back is what was running: a restarted session is a fresh shell, not a
  resumed one.

### Wire protocol

The WebSocket carries two kinds of frames:

- **Binary frames** are raw terminal bytes, in both directions (keystrokes
  up, output and buffer replay down).
- **Text frames** are JSON control messages: `resize` (client → server), and
  `attached` / `exit` / `error` (server → client).

A separate `/ws/events` channel (also cookie-gated) pushes session list
changes — created, updated, gone — so the dashboard stays live without
polling.

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
make dev-backend     # Go backend on :8080, state under ./sandbox/data, --dev
make dev-frontend    # Vite dev server on :5173, proxying /api and /ws to :8080
```

Then open <http://localhost:5173>. The server starts **unlocked** — the
first thing you'll see is a bootstrap form that turns whatever account you
create into the admin. From Settings, the admin can turn on local-host
sessions (today's single-tenant behavior) or self-service registration; add
SSH hosts from the Hosts page to start real sessions.

No vendored Go toolchain? `./env.sh make dev-backend` builds against a
pinned Go (and, for frontend commands, a pinned Node) fetched into `.go/` /
`.node/` instead of whatever's on `$PATH` — see [`env.sh`](env.sh).

## Build & run (production single binary)

```bash
make build                          # build the SPA, embed it, produce ./bin/sessile
./bin/sessile --data-dir=./data     # workspace defaults under data-dir; see Configuration
```

Then open <http://localhost:8080> and bootstrap the admin account.

## Docker

Released images are published to GitHub Container Registry for `linux/amd64`
and `linux/arm64`:

```bash
docker pull ghcr.io/andste82/sessile:latest     # or pin a release tag

mkdir -p data/config data/workspace
docker run -d --name sessile -p 8080:8080 \
  -v "$PWD/data/config:/config" \
  -v "$PWD/data/workspace:/workspace" \
  ghcr.io/andste82/sessile:latest
```

Then open <http://localhost:8080>. Prefer a pinned release tag over
`:latest` for anything you care about. The tags on offer are on the
[releases page](https://github.com/Andste82/sessile/releases).

### Which variant?

Two flavours ship per release, identical in behaviour and configuration —
they differ only in the userland your **local-host sessions** get (SSH
sessions run entirely on the remote host and are unaffected either way):

| Tag | Base | Size | libc | Coreutils |
|---|---|---|---|---|
| `:<version>`, `:latest` | Alpine | ~34 MB | musl | BusyBox |
| `:<version>-ubuntu`, `:latest-ubuntu` | Ubuntu 24.04 | ~112 MB | glibc | GNU |

```bash
docker pull ghcr.io/andste82/sessile:latest-ubuntu
```

### With compose

The repo's own [`docker-compose.yml`](docker-compose.yml) builds from
source and mounts both volumes under one `./data` folder on the host, so
there's still just one thing to back up even though the container sees two
mount points:

```yaml
services:
  sessile:
    image: ghcr.io/andste82/sessile:latest   # pin a release tag, not :latest
    container_name: sessile
    ports:
      - "8080:8080"
    volumes:
      - ./data/config:/config       # config.yml, users.yml, hosts.yml, sessions.db
      - ./data/workspace:/workspace # local-host sandbox, only used if allowLocalHost is on
    restart: unless-stopped
```

Run it with `docker compose up -d`.

### Building it yourself

```bash
docker compose up --build      # build from source and run
make docker                    # alpine variant, tags sessile:dev
make docker-ubuntu             # ubuntu variant, tags sessile:dev-ubuntu
```

The image is multi-stage — Node builds the SPA, Go builds a static binary,
and the runtime layer adds `bash` for local-host shells. Both variants ship
an `/api/health` healthcheck and run `tini` as PID 1. The container runs as
root; local-host sessions run inside it, so treat it as having shell access
to itself and keep it behind a trusted boundary. SSH sessions are unaffected
by this — they run entirely on the remote host you configured.

Check what you are running:

```bash
docker run --rm ghcr.io/andste82/sessile:latest --version
```

## Configuration

Every option is a CLI flag with an environment-variable fallback.

| Flag | Env | Default |
|---|---|---|
| `--addr` | `TSM_ADDR` | `:8080` |
| `--data-dir` | `TSM_DATA_DIR` | `./data` (Docker: `/config`) — `config.yml`, `users.yml`, `users/`, `sessions.db`, `scrollback/`, `history/` |
| `--workspace-dir` | `TSM_WORKSPACE_DIR` | `<data-dir>/workspace` (Docker: `/workspace`) — the local-host sandbox root, reachable only when `allowLocalHost` is on |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` — local-host shell allowlist only; irrelevant unless `allowLocalHost` is on |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` (bytes) |
| `--session-retention` | `TSM_SESSION_RETENTION` | `0` (keep forever); a Go duration, e.g. `720h`, not `30d` |
| `--log-level` | `TSM_LOG_LEVEL` | `info` |
| `--allow-origin` | `TSM_ALLOW_ORIGIN` | *(none)* — one additional origin accepted for WebSocket upgrades |
| `--dev` | `TSM_DEV` | `false` — relaxes the WebSocket origin check, for the Vite dev server |

`--root` has been retired in favor of `--data-dir`/`--workspace-dir` and is
rejected with a pointer to the replacement flags rather than silently
ignored. `--version` prints the version and exits; `--help` lists every
flag.

Everything server- and account-level lives in hand-editable YAML under
`--data-dir`, not behind a flag:

- **`config.yml`** — display name, `allowRegistration`, `allowLocalHost`.
  Created with sensible defaults on first run; editable from Settings once
  logged in as admin.
- **`users.yml`** — username + bcrypt password hash per account.
- **`users/<id>/hosts.yml`** — that user's SSH hosts: name, group, address,
  credentials, target OS, terminal type, and the pinned host-key
  fingerprint. **Credentials are stored in plaintext by design** — see
  [Security](#security--operational-notes).

## REST API

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
```

With `allowLocalHost` on, `{"name":"…","target":"local","directory":"…","shell":"bash"}`
creates a session against the server's own shell instead, sandboxed under
`--workspace-dir` exactly as before this project added SSH support.

The terminal itself attaches over `GET /ws/sessions/:id` (WebSocket, cookie
auth); `GET /ws/events` pushes session list changes. See
[`docs/PROJECT_PLAN.md` §6](docs/PROJECT_PLAN.md) for the full endpoint
list, including account/admin management, host CRUD and the SSH key
exchange endpoint.

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
[`docs/mobile-checklist.md`](docs/mobile-checklist.md) carries the manual
passes: scrolling idle and under load, momentum, the backlog's ends, and
whether a full-screen program scrolls.

GitHub Actions runs `go vet`, `go test`, the frontend build and `vitest` on
every push and pull request — see
[`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Security & operational notes

- **Auth:** username + bcrypt-hashed password, server-side session tokens in
  an **in-memory** store with a **sliding 30-day TTL** (renewed on every
  authenticated request), delivered via an `HttpOnly`, `SameSite=Lax` cookie
  (`Secure` unless `--dev`). A server restart logs everyone out — accepted,
  since nothing about it is persisted by design.
- **Every session and host lookup is scoped to the authenticated user** — a
  client-supplied id you don't own is indistinguishable from one that
  doesn't exist, including for admins: session visibility is strictly
  per-owner, with no cross-user oversight.
- **Host credentials are plaintext in each user's `hosts.yml`, by design,
  not by accident.** The operator is the trusted owner of their own server
  and edits this file by hand. Optional encryption-at-rest is a tracked
  TODO, not built.
- **SSH host-key verification is trust-on-first-use with explicit user
  prompts** — never `ssh.InsecureIgnoreHostKey()`. An unknown or changed key
  blocks the connection (and creates no session) until the user explicitly
  trusts it.
- **"Exchange SSH keys" never persists the password it's given** — used for
  exactly one connection, then discarded; the endpoint clears any previously
  stored password on that host once the exchange succeeds, regardless of
  what the client sends.
- Local-host sessions (when enabled) run under the directory sandbox exactly
  as before this project added SSH support (rejecting `..`, absolute paths
  and symlink escapes), and only from the configured shell allowlist. An SSH
  session's shell/command is the user's own choice on their own host — not
  something this server allowlists.
- **CSRF:** `SameSite=Lax` plus same-origin JSON `fetch`/WS (no CORS origin
  is ever allowed) keeps CSRF risk low without a token.
- Live sessions do not survive a backend restart (see [How it
  works](#how-it-works)). Scrollback (and, for local sessions, command
  history) does, and the session can be restarted from the UI — but the
  process or connection that was running is gone.
- Stopped sessions are kept forever unless you set `--session-retention`.

## License

MIT — see [`LICENSE`](LICENSE).
