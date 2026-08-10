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
- Per-session activity state derived from the session's own PTY (§4.7), so the
  dashboard can say which session is working and which is waiting for input

### Explicitly Out of Scope (do NOT build these, even partially)
- File manager, SFTP, upload/download
- Docker/Kubernetes management
- RDP/VNC, host monitoring (CPU/RAM/disk/service dashboards), server inventory
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
- Terminal: `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`,
  `@xterm/addon-unicode11`, `@xterm/addon-webgl`
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
A stopped session is not a dead end, though: `POST /api/sessions/:id/restart`
gives it a new shell under the same id, with its scrollback and command history
restored from disk (§8). The processes it was running are still gone.

### The critical mechanism: output ring buffer
Reconnect/restore works like this — implement it exactly:

1. Each session owns a **ring buffer of raw PTY output bytes**
   (default 512 KiB, configurable).
2. A single goroutine per session reads from the PTY and, for each chunk:
   a. appends it to the ring buffer,
   b. feeds it to the mode scanner (§4.7) — a byte loop over bytes already
      being copied, which keeps three booleans and stores nothing,
   c. broadcasts it to all attached clients.
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
      events.go             # /ws/events: session state fan-out (see §5)
      protocol.go           # message types (see §5)
    session/
      manager.go            # SessionManager (the core component)
      session.go            # Session struct + lifecycle
      ringbuffer.go
      vtscan.go             # escape-sequence scanner — modes only (§4.7)
      activity.go           # activity classification + sampler (§4.7)
      events.go             # subscriber fan-out for /ws/events
    terminal/
      pty.go                # PTY start/resize/kill wrappers
      foreground_linux.go   # TIOCGPGRP + /proc lookup (§4.7)
      foreground_other.go   # build fallback: reports nothing
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

### 4.7 Session activity (app-agnostic, no emulation)

A running session reports one of three derived states — `busy`, `waiting`,
`idle` — plus the name of the program in the foreground and its working
directory. Nothing here knows about any particular program: adding support for
a new TUI means adding no code.

This is the design tmux arrives at, minus the part tmux needs and we do not.
tmux resolves `#{pane_current_command}` with `tcgetpgrp()` on the pty master
plus a `/proc` lookup (`osdep-linux.c`), and drives `monitor-activity` /
`monitor-bell` / `monitor-silence` from arriving bytes alone. Neither reads
tmux's screen grid. We take both and stop there — see §14.2.

**Three inputs, in order of how much they can be trusted:**

1. **Foreground process group** — `ioctl(TIOCGPGRP)` on the pty **master**,
   then `/proc/<pgid>/comm` and `/proc/<pgid>/cwd`. Exact, not inferred: the
   kernel is the authority on which program owns the terminal. When the
   foreground group is the session's own shell, the shell is at its prompt.
   Sampled once a second, not per byte.

   Take the fd through `File.SyscallConn()`, never `File.Fd()` — `Fd()` pulls
   the file out of the runtime poller and switches it to blocking mode, which
   would quietly break the read loop and `CloseFile()`.

   `cwd` is a path the user did not supply but the UI displays, so it goes
   through the §4.5 sandbox like any other: made relative to root, dropped if
   it resolves outside. A session whose shell has `cd`-ed out of root shows its
   stored `directory` instead.

2. **Output cadence** — time since the last byte left the PTY. No inspection of
   content whatsoever.

3. **Bracketed paste mode** (`ESC[?2004h` / `l`) — scanned out of the byte
   stream by `vtscan.go`. This is the narrow signal for "something is reading a
   line right now": readline, ZLE, fish's reader and Ink-based apps all set it
   while reading and clear it before running. Also scanned: the alternate
   screen modes (already needed by `scrollback.go`) and BEL.

**Classification** — `classify()` in `activity.go` is a pure function over a
snapshot of those inputs, so the whole table below is a unit test:

| # | Condition | State |
|---|---|---|
| 1 | bytes within `busyWindow` (1.5 s) | `busy` |
| 2 | bracketed paste on, foreground **is** the shell | `idle` |
| 3 | bracketed paste on, foreground is **not** the shell, quiet ≥ `waitQuiet` (2.5 s) | `waiting` |
| 4 | foreground **is** the shell | `idle` |
| 5 | BEL within `bellWindow` (60 s) | `waiting` |
| 6 | otherwise | `busy` |

Ahead of all six sits one piece of hysteresis: a session that is already
`waiting`, whose foreground program is still reading a line, stays `waiting`
unless output is **sustained** — present in two consecutive samples rather than
just recent. Programs that are waiting still repaint, and against a real Claude
Code session each repaint otherwise dropped the indicator to `busy` for four
seconds, several times a minute. The asymmetry is deliberate: entering the state
needs positive evidence and a dwell, and so does leaving it. An indicator that
flickers is worse than one that is a second late.

Sustained is counted in sample intervals, not bytes. "Did output keep coming" is
a property every program has; "did more than N bytes arrive" is a threshold that
would need tuning per program, which is the thing this section exists to avoid.

Rule 3's dwell time is what keeps a slow-redrawing program from oscillating:
htop repaints every 1.5 s, so a threshold at the `busy` boundary alone would
flip it back and forth. Rule 6 is the deliberate default — a program that is
running, silent and not visibly prompting is working, not asking. That is why
cursor visibility (`ESC[?25h`) is **not** an input: it is the default state, so
a quiet `go build` would read as a question, and Claude Code inverts it anyway
by hiding the cursor while it waits.

**Measured, not assumed.** These rules were fixed after capturing real PTY
sessions; re-run that capture before changing them.

| Program | bracketed paste | alt screen | cursor | output while waiting |
|---|---|---|---|---|
| bash / zsh / fish | on at prompt, off while running | no | shown | none |
| `sh` (dash) | never — carried by rule 4 | no | shown | none |
| Claude Code | **on while waiting** | no | **hidden** | none |
| htop | never | yes | hidden | repaint every 1.5 s |
| `python3 input()` | never | no | shown | none |

**Known limits, accepted by design.** Constant repainters (`htop`, `top`,
`watch`) read as permanently `busy` — correct under rule 1, and tmux's `#`
flag behaves the same. A program that waits for input without setting
bracketed paste and without ringing the bell (`python3 input()`, a bare `read`
in a script) reads as `busy`. Both are false negatives: the UI stays quiet
instead of claiming attention it does not deserve, which is the direction to
err in. Off Linux there is no `/proc`, so `command` and `cwd` are empty and
only the cadence rules apply.

**Cost.** The scanner runs inline in `broadcast` next to the ring-buffer write
— a byte loop over bytes already being copied, no goroutine and no channel, so
nothing can be dropped. The foreground lookup runs on one manager-wide
goroutine ticking every second, not one per session.

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

- Resize policy: **the smallest attached client wins**. Each client's reported
  geometry is remembered, and the PTY is sized to the smallest rows and the
  smallest cols across every attached client that has reported one, taken per
  axis. Detaching releases that client's constraint; a client that has not
  reported yet constrains nothing.

  This reverses the original "last resize wins, do not implement
  smallest-client negotiation". That rule assumed clients of one size, and
  mirroring a session between a phone and a desktop — the case §5 exists for —
  breaks under it: whichever window resized last sets the width, and every
  other window renders output the program did not write for. Lines wrap where
  they should not, and a full-screen program cleaning up after itself moves the
  cursor over rows it never drew, leaving its screen behind. The smallest size
  fits every window, which is the answer tmux reaches from the same starting
  point. A client with a larger window has unused space, as a tmux client does.
- Keep-alive: server sends WS ping every 30 s, expects pong within 10 s
  (gorilla ping/pong handlers). No JSON-level heartbeat needed.
- Attach sequence, in order: upgrade → send `attached` control frame →
  send ring buffer replay (binary) → begin live streaming. The replay is the
  buffer with the terminal queries filtered out, and `replayBytes` counts what
  is actually sent — see §8, "Replayed output must not ask questions".
- `exit` does **not** close the connection: the session can be restarted
  under the same id, and this is the channel that carries that news to every
  client — including the ones that did not ask for it. Restarting a session
  moves its clients to the new shell, so each of them receives the attach
  sequence again on the connection it already holds. A second `attached` is
  therefore a legitimate mid-connection message and means "live again, reset
  and take the replay". Input sent while stopped is dropped, not an error.
  Only `delete` (4000), shutdown (1001) and a slow consumer (4001) close a
  connection server-side.

Frontend uses plain `WebSocket` with `binaryType = "arraybuffer"`; feed
binary data straight into `terminal.write(new Uint8Array(data))`. Do not use
`@xterm/addon-attach` (its protocol doesn't match ours).

### 5.1 Event channel — `GET /ws/events`

A second endpoint, carrying session **list** state rather than terminal bytes.
It exists because the per-session socket above is open only for a session whose
terminal is on screen, and the dashboard is by definition the page where no
terminal is mounted — so the one socket that could report on a session is the
one whose screen the user is already looking at.

**Text frames only** — this channel never carries binary. Server → client:

```json
{"type":"sessions","sessions":[ …session JSON (§6)… ]}   // full snapshot, sent first
{"type":"session","session":{ …session JSON (§6)… }}     // one session created or changed
{"type":"sessionGone","sessionId":"…"}                   // deleted
```

There are no client → server messages; anything received is discarded. The
connection carries the same 30 s ping / 40 s pong keep-alive and the same
slow-consumer policy as a terminal client (close 4001) — it reuses `ws.Client`
unchanged, so there is still exactly one writer goroutine per connection (§14.3).

Sequence: upgrade → `sessions` snapshot → subscribe → incremental messages.
A `session` message is published on activity change (§4.7), status change,
create, rename and restart; `sessionGone` on delete.

This channel replaces the 5 s list poll of §12 M4. The frontend keeps polling
as a **fallback while the socket is down** — the socket closing is itself the
signal that the list may be stale, and an unreachable backend still has to mark
every session stopped (`markAllStopped`).

---

## 6. REST API (exact spec)

Base path `/api`. All responses JSON. Errors use:
```json
{"error":{"code":"not_found","message":"session not found"}}
```
with appropriate HTTP status (400 validation, 404 missing, 409 conflict,
500 internal, 503 unavailable — refused because the server is shutting down).
A restart refused because the session is already running uses the narrower
code `already_running` (still 409): with several browsers on one session that
race has a loser every time, and the loser wanted a live session and now has
one, so it reconnects rather than reporting a failure.

| Method & Path | Purpose | Notes |
|---|---|---|
| `GET /api/sessions` | List all sessions | Includes `clientCount` per session |
| `POST /api/sessions` | Create | Body below; 201 + session JSON |
| `GET /api/sessions/:id` | Get one | |
| `DELETE /api/sessions/:id` | Kill + remove permanently | 204; also drops the session's scrollback snapshot and history file (§8) |
| `PATCH /api/sessions/:id` | Rename (`{"name":"…"}`) | v0.3, stub not needed earlier |
| `POST /api/sessions/:id/restart` | Give a stopped session a new shell under the same id | No body; 200 + session JSON. 404 unknown, 409 still running, 400 if the directory or shell no longer validates (§8) |
| `GET /api/directories` | Browse dirs under root; optional `?path=` (relative, validated by §4.5) navigates into subdirs | `{"path":"project-a","parent":".","directories":["nested", …]}` — `path` is the cleaned listed path (`.`=root), `parent` is `null` at root |
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
  "rows":32,"cols":120,"clientCount":2,
  "activity":"busy","command":"claude","cwd":"project-a/backend",
  "activitySince":"2026-07-16T12:34:12Z"
}
```

The shape is defined once, in Go, as `session.JSON` with `session.ToJSON(Info)`
— not in `internal/api`. Both the REST handlers and the event channel (§5.1)
serialise it, and `api` imports `ws`, so a shape owned by `api` could not be
reached from either of the packages that push it. `session` already owns the
control-message types for the same reason.

The last four fields come from §4.7 and are runtime-only — none of them is
persisted, and the SQLite schema (§8) is unchanged:

| Field | Meaning |
|---|---|
| `activity` | `"busy"` \| `"waiting"` \| `"idle"`; empty string for a stopped session |
| `command` | foreground program, e.g. `claude`, `htop`, or the shell when at a prompt. Empty where unavailable |
| `cwd` | the shell's actual working directory, relative to root — follows `cd`, unlike `directory`. Empty when unknown or outside root |
| `activitySince` | RFC 3339 UTC; when the session entered its current `activity` |

The bell is deliberately **not** exported. It is an input to rule 5 of §4.7,
not a fourth state: one indicator with three running states is the whole UI
surface (§7).

---

## 7. Frontend Design

### Layout
```
frontend/src/
  api/          # typed fetch wrappers + TS interfaces mirroring §6
  composables/  # useTerminal.ts (xterm setup, WS wiring, fit, reconnect)
                # useSessionEvents.ts (/ws/events → store, §5.1)
  stores/       # sessions.ts (Pinia): list, create, delete, fallback polling
                # ui.ts: view state + persisted client preferences
  components/   # Sidebar.vue, SessionListItem.vue, NewSessionDialog.vue,
                # TerminalView.vue, StatusDot.vue, TabBar.vue
  pages/        # DashboardPage.vue, TerminalPage.vue, SettingsPage.vue
  router/
```

### Pages
- **Dashboard** (`/`): session cards, "New Session" button, root dir shown.
  A card carries the activity indicator, name, shell, the foreground program
  and how long it has been in its current state (§4.7), the working directory
  — `cwd` when known, falling back to the stored `directory` — the client count
  when more than one browser is attached, and last activity.

  The indicator is one component (`StatusDot.vue`) used on the card, in the
  sidebar and in the tab bar, so a session that wants attention is visible from
  inside another session and not only from the dashboard. Four states, reusing
  the palette already in the app rather than introducing colours:

  | State | Rendering |
  |---|---|
  | `stopped` | slate dot |
  | `idle` | emerald dot with glow — the app's existing "running" look |
  | `busy` | emerald dot, `animate-pulse` |
  | `waiting` | amber `?` in place of the dot — amber is already the app's attention colour |

  No browser notifications and no document-title signalling: the indicator is
  the whole surface, per "zero distractions" (§1).
- **Terminal** (`/sessions/:id`): full-height xterm, tab bar of open
  sessions, dark theme default.
- **Settings** (`/settings`): read-only server config display, plus the client
  preferences the browser owns — the terminal font size (8–32 px, default 13),
  with a live sample, and copy-on-select (default on). Alongside the
  copy-on-select toggle sits a short clipboard help: the copy, paste and
  interrupt keys for this platform, and the two rules that surprise every
  newcomer — Ctrl+C never copies, and Ctrl+Shift+C is also the browser's
  devtools shortcut. It is deliberately three rows and a caveat, not
  documentation. Preferences live in `stores/ui.ts` and
  persist to `localStorage` under `sessile.*`, not on the server: the readable
  size depends on the screen in front of the user, so the same session opened
  on a phone and on a desktop wants two different answers. A change applies to
  open terminals immediately (`term.options.fontSize`, then a fit, so the PTY
  is told the new column count), and to the app's other tabs through the
  `storage` event — tabs mirror a session (§5), so one of them staying at the
  old size reads as the setting not having taken.

### Terminal behavior (`useTerminal`)
- Create `Terminal` with `scrollback: 5000`, load fit + web-links addons.
- Rendering: load `@xterm/addon-webgl` after `terminal.open()` — the renderer
  VS Code's terminal uses. Every failure path falls back to xterm's DOM
  renderer, which is the slow one, not the wrong one: `loadAddon` throws where
  no WebGL2 context can be had (old devices, blocklisted drivers, acceleration
  switched off), and a context can be withdrawn later, since mobile browsers
  reclaim GPU memory from backgrounded tabs. Dispose the addon in
  `onContextLoss`, or the terminal stops painting instead of getting slower.
- Fit on mount + on `ResizeObserver` change; after fit, send `resize`
  control frame.
- `terminal.onData(d => ws.send(encoder.encode(d)))` (binary).
- Touch scrolling routes the same way a mouse wheel does on a desktop, which is
  the behaviour to match (`utils/gesture.ts`): the backlog scrolls for a shell,
  and for a program drawing its own screen — the alternate screen, or any mouse
  tracking mode above x10 — the drag is dispatched to xterm as a `wheel` event,
  which encodes it as a mouse report or as cursor keys. Without that second
  path a TUI does not react to being scrolled at all, since the alternate
  screen has no scrollback to move.
- On WS close (not user-initiated): show a "Disconnected — reconnecting…"
  overlay; retry with exponential backoff (1 s → 2 s → 4 s → max 15 s).
  On reattach, call `terminal.reset()` before replay so buffer replay
  renders cleanly.
- On `exit` control frame: banner "Session ended", disable input. The socket
  stays open (§5) and an `attached` frame on it clears the banner — that is
  how a restart from another client reaches this one.
- A close of 4000/4404 is not retried: another attempt only earns another
  4404. A session that comes back reaches this client on the two paths that
  already exist — the `attached` frame above for a socket that survived, and
  the polled session list, which the terminal page watches so a terminal whose
  socket did not survive reconnects when the list says the session runs again.
- Focus: the terminal takes keyboard focus when a session becomes active —
  which is on mount, since the terminal component is keyed on the route id
  and a session switch remounts it. Pointer devices only (`hover: hover and
  pointer: fine`): on a touchscreen, focusing opens the virtual keyboard, and
  it must not spring up on every session switch.
- Character width: `@xterm/addon-unicode11` is loaded and
  `unicode.activeVersion` set to `'11'` (which needs `allowProposedApi`).
  xterm's built-in table is Unicode 6 and gives width 1 to everything above
  U+FFFF outside CJK planes 2–3, so emoji were allotted one cell, drew two,
  and the following character overwrote the right half. Unicode 11 is also
  what the programs in the PTY assume — glibc's `wcwidth` has been Unicode 9+
  for years — so the two ends agree on the column count. The font stack ends
  in the platform emoji faces for the same reason: a correct cell count still
  clips if the glyph is drawn from a proportional fallback.
- IME / predictive keyboards: xterm's three composition paths (`_inputEvent`,
  `CompositionHelper.keydown` textarea diffing, `_finalizeComposition`) all
  leak intermediate states on mobile keyboards, so `useTerminal` owns the
  sequence instead. Composition events and composition `input` are withheld
  from xterm (never `preventDefault`ed — the browser must keep editing the
  helper textarea), and the textarea's value is delivered once: after 40 ms of
  keyboard quiet, or immediately when a real key ends the word. Only committed
  text reaches the PTY.
- Copy: copy-on-select (a left-button drag that ends with a non-empty
  selection puts it on the clipboard; on by default, switchable in Settings),
  the context menu, Ctrl+Shift+C and Ctrl+Insert (Cmd+C on macOS, which the
  browser handles itself). Ctrl+C is always SIGINT, never a copy — even with a
  selection: a selection outlives the drag that made it, so a Ctrl+C that
  copies is a Ctrl+C that fails to interrupt exactly when it is needed most.
  Copy-on-select earns its default from the other end of the same problem —
  Ctrl+Shift+C is the browser's devtools shortcut and no page can take it.
  The mouseup is listened for on the document so a drag may end outside the
  terminal, and gated on a mousedown inside it so a selection made elsewhere
  in the app never copies the terminal's leftovers.
- Paste: Ctrl+V / Ctrl+Shift+V /
  Shift+Insert (Cmd+V on macOS), context menu, and the mobile keyboard's
  clipboard menu — all funnelled through `terminal.paste()`, so pasted text
  is sent as binary input with bracketed-paste framing when the foreground
  app enabled it. The paste chords are handed back to the browser via
  `attachCustomKeyEventHandler`; xterm would otherwise send ^V and cancel the
  keydown, which suppresses the native paste.

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

### Scrollback and command history (restart restore)

"Metadata only" above governs SQLite, and it still does. Two things live in
files beside the database instead, because they are what makes a stopped
session worth reopening:

```
<db-dir>/
  sessions.db
  scrollback/<session-id>.bin   # raw ring-buffer snapshot
  history/<session-id>          # HISTFILE for bash/zsh sessions
```

The directory is `--data-dir`, resolved to an absolute path, and is
deliberately **outside `--root`**: a shell that could read or rewrite its own
history file inside the sandbox would make the restored history worthless, and
a relative path would resolve against the *session's* directory once it reaches
a shell's environment.

- **Scrollback.** The ring buffer is written out when a shell exits, on
  graceful shutdown, and on a timer (reusing `activityThrottle`) so a SIGKILLed
  backend still restores output up to the last flush. It is not in SQLite: the
  payload is opaque, up to `--buffer-size` per session and rewritten
  repeatedly — exactly the write pattern that would make a single-connection
  SQLite the bottleneck for the PTY read loop.
- **Command history.** Each session's shell is pointed at its own history
  through the environment: `HISTFILE` (+`HISTSIZE`/`HISTFILESIZE` and
  `PROMPT_COMMAND=history -a`) for bash, `HISTFILE`+`HISTSIZE`+`SAVEHIST` for
  zsh, `fish_history` for fish. Reusing the session id across a restart is what
  makes arrow-up replay that session's own commands. bash and zsh flush from
  the exit path SIGHUP runs — which is what the kernel delivers when the
  backend dies and the PTY master closes — and fish appends after every
  command.
  Known limit, documented in the README: a user rc file that assigns `HISTFILE`
  itself (oh-my-zsh does) wins over the environment, and such a session falls
  back to the shared history file.

Neither grows without bound. A session's ring buffer is released the moment it
stops — it is unreachable from then on, since Attach rejects anything not
running, and the snapshot on disk is what a restart reads. Rows and their files
are discarded by `--session-retention` on startup, which is **off by default**:
a stopped session is no longer a dead end now that it can be restarted with its
output and history, so expiring one is an operator's decision.

**Replayed output must not ask questions.** PTY output is not only drawing:
`ESC [ c` asks the terminal what it is, `ESC [ 6 n` where the cursor is,
`ESC ] 11 ; ?` what the background colour is, `ESC ] 52 ; c ; ?` what is on the
clipboard. Live, that is a conversation the program that asked is there to
finish. Replayed, only the question survives — the emulator answers it into the
PTY, and what reads the PTY now is a fresh shell at a prompt. Claude Code emits
`ESC [ > 0 q ESC [ c` on startup, xterm.js answers the replay with
`ESC [ ? 1 ; 2 c`, and the shell leaves `1;2c` on its command line, once per
attach — every reload, every extra tab, every reconnect, and across restarts,
since the restart seeds the new buffer from the same snapshot.

Every replay is therefore filtered (`sanitizeReplay`, `internal/session`): the
device-attribute, status, mode, capability, window and colour/clipboard queries
are dropped, and everything that draws or sets is passed through byte for byte.
A query renders nothing, so the restored screen is unchanged. The filter runs
on the replay only — live output keeps its queries, or a running program would
wait forever for an answer that was deleted.

`POST /api/sessions/:id/restart` (§6) spawns a new shell under the same id,
name, directory and shell, re-running the §4.5 sandbox and allowlist checks.
The new ring buffer is pre-seeded with the snapshot followed by a separator
that leaves the alternate screen and resets attributes, cursor and autowrap —
without it a session that stopped inside a full-screen program would leave the
replacement shell drawing into an unreachable screen. Restart is always
explicit: sessions are never respawned automatically on startup.

The separator also turns the **input modes** back off: mouse tracking
(1000/1002/1003 and the 1005/1006/1015/1016 encodings), focus reporting (1004),
bracketed paste (2004), application cursor keys and keypad, and the scroll
margins. A program that exits cleanly does this itself; one that is killed does
not, and the snapshot then ends with them on. Inherited by a shell, they are not
cosmetic — with mouse tracking on, every mouse *move* across the window sends
the shell a report that it echoes as `35;42;7M` plus a bell, faster than
anything can be typed. Two of these resets move the cursor as a side effect and
so are handled specially: DECRST 1049 is conditional on the snapshot really
ending in the alternate screen, and DECSTBM is wrapped in DECSC/DECRC. A full
reset (RIS) is not an option — it would clear the very history the separator
introduces.

---

## 9. Configuration

| Flag | Env | Default |
|---|---|---|
| `--root` | `TSM_ROOT` | required |
| `--addr` | `TSM_ADDR` | `:8080` |
| `--data-dir` | `TSM_DATA_DIR` | `<root>/.tsm` → in Docker: `/config`. Holds `sessions.db`, `scrollback/`, `history/` |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` (allowlist) |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` (bytes) |
| `--session-retention` | `TSM_SESSION_RETENTION` | `0` (keep forever); Go duration, e.g. `720h` |
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
- Caching: `/assets/*` is content-hashed by the build, so it is served
  `public, max-age=31536000, immutable`. `index.html` — one URL naming the
  current bundles — and any unhashed file are served `no-cache` with an ETag,
  so every load revalidates and the usual answer is a 304. Serving the index
  with no cache headers lets a browser pick its own freshness lifetime and
  keep running the previous frontend against an upgraded backend.
- Makefile targets: `make dev-backend`, `make dev-frontend`, `make build`,
  `make test`, `make docker`.
- Version: injected at build time via `-ldflags -X …config.Version`. The
  release passes it in from the git tag; otherwise the Makefile derives it from
  `git describe --tags --always --dirty`, so a non-release build names the
  commit it came from rather than calling itself "dev". The local image tag is
  a separate knob (`IMAGE_TAG`, default `dev`) so it stays stable across
  rebuilds.

### Docker (multi-stage)
1. `node:22-alpine` → build frontend
2. `golang:1.22-alpine` → copy `dist` in, `CGO_ENABLED=0 go build`
3. Runtime: `alpine:3` (needs `bash` installed for shells; scratch won't work
   since sessions need real shells) — copy single binary.
   `EXPOSE 8080`; volumes `/config`, `/workspace`;
   `HEALTHCHECK` hitting `/api/health`;
   default cmd: `server --root=/workspace --data-dir=/config`.

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

### M7 — Session activity + dashboard overview
Escape-scanner and foreground-process lookup (§4.7), activity classification
and sampler, the event fan-out and `/ws/events` (§5.1), the four new session
fields (§6), the four-state indicator and the rebuilt dashboard card (§7).
The list poll becomes a fallback for when the event socket is down.
✅ *Verify:* in one session run `sleep 20` → the dot pulses and the card names
`sleep`; back at the prompt → steady dot, card names the shell. `cd` into a
subdirectory → the card follows. Start `claude` in a second session, let it ask
something → amber `?`, visible from inside the first session's tab bar. Create
a session in a second browser tab → it appears in the first at once, not after
5 s. Stop the backend → every dot goes slate; start it → the socket reconnects
and the states come back. `htop` reads as permanently `busy` — expected (§4.7).

### v0.3+ (later, do not start now)
Search/filter, favorites, rename (PATCH), then v0.4 auth/multi-user/roles/
audit log. Future: SSH remotes, tmux import, session sharing, read-only mode.

---

## 13. Testing Strategy

- **Unit (Go):** RingBuffer (wraparound, exact-boundary), path sandbox
  (§4.5 cases), shell allowlist, session state transitions, the escape scanner
  (§4.7 — sequences split across chunk boundaries and BEL as an OSC terminator
  are the two cases a naive scanner gets wrong), `classify()` as a table over
  all six rules, and the event fan-out including its slow-subscriber drop.
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
2. Raw byte replay via ring buffer — no server-side terminal emulation. The
   server holds no character grid, no cursor and no cell attributes; there is
   exactly one terminal emulator in the system and it is the xterm.js the user
   is looking at. §4.7's scanner does not weaken this: it reads mode switches
   out of the stream and keeps three booleans, and it can answer "is something
   reading a line" but never "what is on the screen".

   This is the line that keeps the dashboard from showing a preview of each
   session's screen. That feature needs `tmux capture-pane`, which needs
   tmux's `grid.c` — and a second, less complete emulator would disagree with
   xterm.js exactly in the cases where the answer matters, with no way for the
   user to tell which one is lying.
3. One writer goroutine per WS connection; broadcasts never block.
4. Every user-supplied path goes through the sandbox function. No exceptions.
5. Decisions in this spec are final for v0.1–0.2; do not introduce
   alternative libraries or extra features without updating this document.
