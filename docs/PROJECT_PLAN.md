# SSH Session Manager — Implementation Spec

A lightweight, browser-based terminal session manager with persistent sessions.
Think **tmux + VS Code integrated terminal, in the browser**.

This document is written to be directly implementable. All technology choices
are final (no either/or options). Build milestone by milestone, in order, and
verify each milestone's acceptance criteria before moving on.

---

## 1. Product Definition

### In Scope
- Persistent local terminal sessions (PTY) that survive browser disconnects
- Web UI (responsive SPA) to create, list, attach, and kill sessions
- Multiple simultaneous clients attached to the same session
- Scrollback restoration on reconnect

### Explicitly Out of Scope (do NOT build these, even partially)
- File manager, SFTP, upload/download
- Docker/Kubernetes management
- RDP/VNC, monitoring, server inventory
- Script execution framework
- Remote SSH sessions (future — design must not block it, but write zero SSH code now)

### Guiding Principle
> One browser. One terminal. Persistent sessions. Zero distractions.

---

## 2. Technology Stack (final decisions)

### Backend — Go 1.22+
| Concern | Choice | Rationale |
|---|---|---|
| HTTP framework | `github.com/gin-gonic/gin` | Simple routing + middleware |
| WebSocket | `github.com/gorilla/websocket` | Mature, actively maintained, well-documented |
| PTY | `github.com/creack/pty` | De-facto standard |
| SQLite driver | `modernc.org/sqlite` | Pure Go — **no CGO**, keeps the static binary trivial |
| DB access | stdlib `database/sql` with hand-written queries | Only 2 tiny tables; an ORM is overkill |
| Logging | stdlib `log/slog` (JSON handler) | No dependency needed |
| Config | CLI flags via stdlib `flag`, with env-var fallbacks | See §9 |
| Frontend embedding | `embed.FS` (`//go:embed`) | Single-binary distribution |

Do **not** add GORM, sqlc, zap, or viper.

### Frontend — Vue 3 + TypeScript + Vite
- `vue@3`, `vue-router@4`, `pinia`
- `tailwindcss` (v4, via `@tailwindcss/vite` plugin)
- `@headlessui/vue`, `@heroicons/vue`
- `@vueuse/core`
- Terminal: `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`
  (note: the packages are scoped `@xterm/*`; the old unscoped `xterm` packages are deprecated)

Dev mode: Vite dev server proxies `/api` and `/ws` to the Go backend (see §10).
Prod mode: Go serves the built frontend from `embed.FS`.

---

## 3. Architecture Overview

```
Browser (xterm.js) ⇄ WebSocket ⇄ Go backend ⇄ PTY ⇄ shell process
                     REST (JSON) ⇅
                              SQLite (metadata only)
```

**Core invariant:** the PTY and shell live in the backend process. Browser
connections are ephemeral views onto it. Killing every browser tab must not
affect the shell.

**Consequence:** live sessions cannot survive a *backend* restart. On startup,
any session marked `running` in SQLite is transitioned to `stopped`
(the process is gone). This is accepted for v0.x — document it in the README.

### The critical mechanism: output ring buffer
Reconnect/restore works like this — implement it exactly:

1. Each session owns a **ring buffer of raw PTY output bytes**
   (default 512 KiB, configurable).
2. A single goroutine per session reads from the PTY and, for each chunk:
   a. appends it to the ring buffer,
   b. broadcasts it to all attached clients.
3. When a client attaches, the server first sends the **entire current ring
   buffer contents** as one or more binary frames, then streams live output.
4. xterm.js re-renders ANSI sequences from that replay, restoring colors,
   cursor position, and scrollback "for free". No terminal emulation is done
   server-side.

Ring buffer implementation: a simple `[]byte` with copy-on-overflow is fine
(`if len(buf)+len(chunk) > max { drop oldest bytes }`). Guard with a mutex.

---

## 4. Backend Design

### 4.1 Project layout
```
backend/
  cmd/server/main.go        # flag parsing, wiring, graceful shutdown
  internal/
    api/                    # Gin handlers + router setup + middleware
      router.go
      sessions.go           # REST handlers
      directories.go
      errors.go             # unified error responses
    ws/                     # WebSocket endpoint + client pumps
      handler.go
      client.go             # read pump / write pump per client
      protocol.go           # message types (see §5)
    session/
      manager.go            # SessionManager (the core component)
      session.go            # Session struct + lifecycle
      ringbuffer.go
    terminal/
      pty.go                # PTY start/resize/kill wrappers
    storage/
      sqlite.go             # open + migrate
      sessions.go           # CRUD queries
    config/
      config.go
frontend/                   # see §7
Dockerfile
docker-compose.yml
README.md
CLAUDE.md
```

### 4.2 Core types

```go
type Status string

const (
    StatusRunning Status = "running"
    StatusStopped Status = "stopped" // process exited or was killed
)

type Session struct {
    ID           string    // UUID v4
    Name         string
    Directory    string    // relative to root, e.g. "project-a"
    Shell        string    // "bash" | "zsh" | "fish"
    Status       Status
    PID          int
    Created      time.Time
    LastActivity time.Time
    Rows, Cols   uint16

    // runtime-only (never persisted)
    cmd     *exec.Cmd
    pty     *os.File
    buffer  *RingBuffer
    clients map[*Client]struct{}
    mu      sync.Mutex
}
```

### 4.3 SessionManager responsibilities
- `Create(name, dir, shell) (*Session, error)` — validates dir (§4.5) and
  shell (must be in allowlist and exist on PATH), starts PTY, spawns the
  read-broadcast goroutine, persists metadata, returns session.
- `Get(id)`, `List()` — from in-memory map, merged with `stopped` rows from
  SQLite that are no longer in memory after restart.
- `Delete(id)` — SIGTERM the process group, 5 s grace, then SIGKILL; close
  PTY; disconnect clients with an `exit` control message; delete DB row.
- `Attach(id, client)` / `Detach(id, client)`.
- Store: `map[string]*Session` guarded by `sync.RWMutex`.

### 4.4 Concurrency model (follow exactly)
- **1 goroutine per session**: `pty.Read` loop → buffer + broadcast.
  On read error (EOF = process exited): mark session `stopped`, update DB,
  send `{"type":"exit"}` control frame to clients, close their write channels.
- **2 goroutines per client** (standard gorilla pattern):
  - *read pump*: reads WS frames → binary frames are written to the PTY;
    text frames are parsed as JSON control messages (§5).
  - *write pump*: single goroutine draining a buffered `chan []byte`
    (capacity ~256) → WS. This is the **only** writer to that WS connection
    (gorilla requires a single writer). If a client's channel is full,
    disconnect that client (slow-consumer policy).
- Broadcast never blocks on a slow client: non-blocking channel send, drop
  the client on overflow.
- PTY writes from multiple clients are serialized by a mutex on the PTY.

### 4.5 Directory sandbox (security-critical)
Root is given at startup (`--root=/workspace`). For any user-supplied
directory:
1. Reject empty, absolute paths, and any path containing `..` segments.
2. `full := filepath.Join(root, filepath.Clean(userPath))`
3. `resolved, err := filepath.EvalSymlinks(full)` — must succeed.
4. Require `resolved == root` or `strings.HasPrefix(resolved, rootResolved + string(os.PathSeparator))`
   where `rootResolved = filepath.EvalSymlinks(root)`.
5. Must be an existing directory.
Write unit tests for: `..`, `../..`, absolute paths, symlinks pointing
outside root, and valid nested paths.

### 4.6 Session lifecycle & housekeeping
- `LastActivity` updated on any PTY read or client input (throttle DB writes
  to at most once per 30 s).
- No automatic session killing in v0.1 (persistence is the point). Optional
  `--idle-timeout` flag may be added later; default off.
- Graceful shutdown: on SIGTERM, mark all running sessions `stopped` in DB,
  SIGTERM child process groups, close WS connections, then exit.
- Start shells with `Setsid`/process group so `Delete` can kill the whole tree.

---

## 5. WebSocket Protocol (exact spec)

Endpoint: `GET /ws/sessions/:id` (upgraded). Connecting to a `stopped`
session is rejected with WS close code 4404 + reason.

Framing rules:
- **Binary frames** carry raw terminal bytes, both directions
  (client→server: keystrokes; server→client: PTY output / buffer replay).
- **Text frames** carry JSON control messages:

Client → Server:
```json
{"type":"resize","cols":120,"rows":32}
```
Server → Client:
```json
{"type":"attached","sessionId":"…","replayBytes":48213}
{"type":"exit"}                       // process ended; UI shows "stopped"
{"type":"error","message":"…"}
```

- Resize policy: **last resize wins** (any client resizing applies to the
  PTY via `pty.Setsize` and is stored on the session). Keep it simple; do
  not implement smallest-client negotiation.
- Keep-alive: server sends WS ping every 30 s, expects pong within 10 s
  (gorilla ping/pong handlers). No JSON-level heartbeat needed.
- Attach sequence, in order: upgrade → send `attached` control frame →
  send ring buffer replay (binary) → begin live streaming.

Frontend uses plain `WebSocket` with `binaryType = "arraybuffer"`; feed
binary data straight into `terminal.write(new Uint8Array(data))`. Do not use
`@xterm/addon-attach` (its protocol doesn't match ours).

---

## 6. REST API (exact spec)

Base path `/api`. All responses JSON. Errors use:
```json
{"error":{"code":"not_found","message":"session not found"}}
```
with appropriate HTTP status (400 validation, 404 missing, 409 conflict,
500 internal).

| Method & Path | Purpose | Notes |
|---|---|---|
| `GET /api/sessions` | List all sessions | Includes `clientCount` per session |
| `POST /api/sessions` | Create | Body below; 201 + session JSON |
| `GET /api/sessions/:id` | Get one | |
| `DELETE /api/sessions/:id` | Kill + remove permanently | 204 |
| `PATCH /api/sessions/:id` | Rename (`{"name":"…"}`) | v0.3, stub not needed earlier |
| `GET /api/directories` | List dirs one level under root | `{"directories":["project-a", …]}` |
| `GET /api/config` | Root path, available shells, version | Shells = allowlist ∩ installed |
| `GET /api/health` | `{"status":"ok"}` | For Docker healthcheck |

Create body:
```json
{"name":"Backend","directory":"project-a","shell":"bash"}
```
Validation: name 1–64 chars; directory passes §4.5; shell in allowlist.

Session JSON shape (single source of truth — mirror in TS types):
```json
{
  "id":"…","name":"Backend","directory":"project-a","shell":"bash",
  "status":"running","pid":12345,
  "created":"2026-07-16T12:00:00Z","lastActivity":"2026-07-16T12:34:56Z",
  "rows":32,"cols":120,"clientCount":2
}
```

---

## 7. Frontend Design

### Layout
```
frontend/src/
  api/          # typed fetch wrappers + TS interfaces mirroring §6
  composables/  # useTerminal.ts (xterm setup, WS wiring, fit, reconnect)
  stores/       # sessions.ts (Pinia): list, create, delete, polling
  components/   # Sidebar.vue, SessionListItem.vue, NewSessionDialog.vue,
                # TerminalView.vue, StatusDot.vue, TabBar.vue
  pages/        # DashboardPage.vue, TerminalPage.vue, SettingsPage.vue
  router/
```

### Pages
- **Dashboard** (`/`): session cards (name, status dot, directory, last
  activity, client count), "New Session" button, root dir shown.
- **Terminal** (`/sessions/:id`): full-height xterm, tab bar of open
  sessions, dark theme default.
- **Settings** (`/settings`): read-only config display for now.

### Terminal behavior (`useTerminal`)
- Create `Terminal` with `scrollback: 5000`, load fit + web-links addons.
- Fit on mount + on `ResizeObserver` change; after fit, send `resize`
  control frame.
- `terminal.onData(d => ws.send(encoder.encode(d)))` (binary).
- On WS close (not user-initiated): show a "Disconnected — reconnecting…"
  overlay; retry with exponential backoff (1 s → 2 s → 4 s → max 15 s).
  On reattach, call `terminal.reset()` before replay so buffer replay
  renders cleanly.
- On `exit` control frame: banner "Session ended", disable input.
- Copy: xterm default selection copy. Paste: Ctrl+Shift+V / context menu
  (send pasted text as binary input).

### Responsiveness
- ≥1024 px: persistent sidebar | terminal.
- 640–1024 px: collapsible icon sidebar.
- <640 px: bottom navigation (Dashboard / Terminal / Settings),
  full-screen terminal, horizontally scrollable tab bar, 44 px+ touch
  targets.

New-session flow: single dialog with name input, directory `<select>`
(from `/api/directories`), shell `<select>` (from `/api/config`) → create
→ navigate straight to the terminal page.

---

## 8. Persistence (SQLite)

Metadata only. Schema (run as embedded migration on startup):

```sql
CREATE TABLE IF NOT EXISTS sessions (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  directory     TEXT NOT NULL,
  shell         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'running',
  created       TEXT NOT NULL,          -- RFC 3339 UTC
  last_activity TEXT NOT NULL
);
```
App config lives in flags/env (§9), not the DB — drop the config table from
the original plan; it adds state with no benefit.

On startup: `UPDATE sessions SET status='stopped' WHERE status='running';`

---

## 9. Configuration

| Flag | Env | Default |
|---|---|---|
| `--root` | `TSM_ROOT` | required |
| `--addr` | `TSM_ADDR` | `:8080` |
| `--db` | `TSM_DB` | `<root>/.tsm/sessions.db` → in Docker: `/config/sessions.db` |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` (allowlist) |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` (bytes) |
| `--log-level` | `TSM_LOG_LEVEL` | `info` |

---

## 10. Dev & Build Workflow

- `backend`: `go run ./cmd/server --root=$(pwd)/../sandbox`
- `frontend`: `npm run dev` with Vite proxy:
  ```ts
  server: { proxy: {
    '/api': 'http://localhost:8080',
    '/ws':  { target: 'ws://localhost:8080', ws: true },
  }}
  ```
- Prod: `npm run build` → output copied/embedded into Go binary via
  `//go:embed` of `frontend/dist` (a small `web/embed.go` in backend);
  SPA fallback: unknown non-`/api`, non-`/ws` GETs serve `index.html`.
- Makefile targets: `make dev-backend`, `make dev-frontend`, `make build`,
  `make test`, `make docker`.

### Docker (multi-stage)
1. `node:22-alpine` → build frontend
2. `golang:1.22-alpine` → copy `dist` in, `CGO_ENABLED=0 go build`
3. Runtime: `alpine:3` (needs `bash` installed for shells; scratch won't work
   since sessions need real shells) — copy single binary.
   `EXPOSE 8080`; volumes `/config`, `/workspace`;
   `HEALTHCHECK` hitting `/api/health`;
   default cmd: `server --root=/workspace --db=/config/sessions.db`.

---

## 11. Security (v0.1 baseline)

- Directory sandbox per §4.5 (tested).
- Shell allowlist — never exec a user-supplied path.
- WebSocket origin check: same-origin by default, `--allow-origin` flag to
  override (needed for Vite dev — allow `http://localhost:5173` when
  `--dev` flag set).
- Body size limits on JSON endpoints (e.g. 4 KiB).
- No auth in v0.1; deploy behind a reverse proxy. JWT auth arrives in v0.4 —
  leave an `internal/auth` package with a no-op middleware so wiring exists.
- Rate limiting & CSRF: defer to v0.4 with auth (CSRF is moot without
  cookies; the API is same-origin fetch + WS).

---

## 12. Milestones (implement in this order)

Each milestone must compile, pass `go vet` + tests, and meet its acceptance
criteria before starting the next.

### M0 — Scaffold
Repo layout (§4.1), Go module, Gin server with `/api/health`, Vue+Vite+
Tailwind app rendering a placeholder, Makefile, Vite proxy.
✅ *Verify:* `curl localhost:8080/api/health` returns ok; `npm run dev`
shows the placeholder and proxies `/api/health`.

### M1 — PTY sessions + WebSocket (backend only, the core)
SessionManager, Session, RingBuffer, PTY start, WS protocol (§5),
in-memory only (no SQLite yet). Unit tests for RingBuffer and path sandbox.
✅ *Verify with a script* (`scripts/wstest.sh` using `websocat` or a tiny Go
test client): create session via curl → connect WS → send `ls\n` as binary →
receive output → disconnect → run `echo hi` via a second connection → confirm
first session's replay contains earlier output.

### M2 — REST completeness + SQLite
All §6 endpoints, storage layer, startup reconciliation, graceful shutdown,
error format, directory listing.
✅ *Verify:* full curl walkthrough documented in README works; restart backend
→ old sessions listed as `stopped`.

### M3 — Frontend terminal (single session, desktop)
Dashboard list + create dialog + terminal page with working xterm, resize,
replay on refresh.
✅ *Verify:* create session in UI, run `htop`, refresh the page, htop still
rendering; close tab, reopen, scrollback intact.

### M4 — Reconnect & multi-client polish
Auto-reconnect with backoff, exit banners, multiple simultaneous clients,
client count live in list (poll every 5 s), slow-consumer handling.
✅ *Verify:* two browser windows on one session mirror each other; kill
backend → UI shows reconnecting; restart → session shows stopped.

### M5 — Tabs, responsive/mobile UI, dark mode polish
Tab bar, bottom nav <640 px, touch targets, sidebar states, favicon/title.
✅ *Verify:* Chrome device-mode iPhone + iPad pass a manual checklist
(documented in `docs/mobile-checklist.md`).

### M6 — Docker + release
Multi-stage Dockerfile, compose file, README (features, screenshots later,
config table, security notes, backend-restart caveat).
✅ *Verify:* `docker compose up` → full workflow works from a clean machine.

### v0.3+ (later, do not start now)
Search/filter, favorites, rename (PATCH), then v0.4 auth/multi-user/roles/
audit log. Future: SSH remotes, tmux import, session sharing, read-only mode.

---

## 13. Testing Strategy

- **Unit (Go):** RingBuffer (wraparound, exact-boundary), path sandbox
  (§4.5 cases), shell allowlist, session state transitions.
- **Integration (Go):** `httptest` + real PTY: create → attach → I/O →
  replay → delete. Use `sh -c 'echo READY; cat'` as a deterministic shell
  for tests instead of bash.
- **Frontend:** keep it light — `vitest` for the API layer and the WS
  message codec; no E2E framework in v0.x.
- CI (GitHub Actions): `go vet`, `go test ./...`, `npm run build`,
  `npm run test` on push.

---

## 14. Non-negotiable Design Principles

1. Backend owns the terminals; the browser is a dumb view.
2. Raw byte replay via ring buffer — no server-side terminal emulation.
3. One writer goroutine per WS connection; broadcasts never block.
4. Every user-supplied path goes through the sandbox function. No exceptions.
5. Decisions in this spec are final for v0.1–0.2; do not introduce
   alternative libraries or extra features without updating this document.
