# Terminal Host Session Service — Implementation Spec

A lightweight, browser-based, **multi-user** terminal session manager with
persistent sessions against SSH-reachable hosts. Think **tmux + VS Code
integrated terminal, in the browser, per user, per host**.

This document is written to be directly implementable. All technology choices
are final (no either/or options). Build milestone by milestone, in order, and
verify each milestone's acceptance criteria before moving on.

> **v0.4 scope change.** Milestones M0–M7 below shipped the original
> single-tenant, local-shell-only design. Starting at M8 (§12b), the project
> deliberately pivots to multi-user accounts and per-user SSH host sessions.
> Sections below are updated in place to describe the system as it now
> stands; §12 keeps the original milestone history for record, and §12b holds
> the new one.

---

## 1. Product Definition

### In Scope
- Multi-user accounts: username/password login, an "unlocked" first-run that
  lets the first user become admin, optional self-service registration
  (admin-controlled)
- Per-user SSH host configuration: name, group, address, auth (password
  and/or private key), target OS, terminal/shell type
- Persistent terminal sessions (PTY) against those SSH hosts, or — when an
  admin enables it — against the server's own local shell, that survive
  browser disconnects
- SSH host-key trust-on-first-use, with explicit user prompts on first
  connect and on any host-key change
- An "Exchange SSH keys" host action that sets up passwordless login without
  ever persisting the password used to set it up
- Web UI (responsive SPA) to create, list, attach, and kill sessions
- Multiple simultaneous clients attached to the same session
- Scrollback restoration on reconnect
- Per-session foreground program and working directory for local sessions,
  read from the session's own PTY (§4.7) — not available for SSH sessions,
  see §4.7's note — plus the window title the program sets for itself (§4.8),
  available for both local and SSH sessions since it is scanned out of the
  output stream rather than read from /proc
- A session's process tree, and a file browser (list/move/copy/delete on the
  target, download a file to the browser, upload one back) for its target —
  local or SSH, Linux or Windows (§4.10). Both ride the session's own identity
  and, for SSH, its own already-trusted connection — neither is a general
  remote-command or file-transfer tool, see Out of Scope below

### Explicitly Out of Scope (do NOT build these, even partially)
- An in-app text editor for files reached through §4.10 — the read/write path
  that download/upload already needs is not an editor; that is a later,
  separate decision layered on top, not implied by this one
- A generic remote-command endpoint, or any file operation that takes more
  than one explicit src/dst/path — §4.10's operations are a fixed, named set
  for exactly that reason
- Docker/Kubernetes management
- RDP/VNC, host monitoring (CPU/RAM/disk/service dashboards), server inventory
- Encryption-at-rest for stored host credentials (tracked as a TODO — see
  §11 — not built in v0.4)

### Guiding Principle
> One browser. Your terminals, your hosts. Persistent sessions. Zero
> distractions.

---

## 2. Technology Stack (final decisions)

### Backend — Go 1.22+
| Concern | Choice | Rationale |
|---|---|---|
| HTTP framework | `github.com/gin-gonic/gin` | Simple routing + middleware |
| WebSocket | `github.com/gorilla/websocket` | Mature, actively maintained, well-documented |
| PTY | `github.com/creack/pty` | De-facto standard |
| SQLite driver | `modernc.org/sqlite` | Pure Go — **no CGO**, keeps the static binary trivial |
| DB access | stdlib `database/sql` with hand-written queries | Session metadata only; an ORM is overkill |
| Auth hashing | `golang.org/x/crypto/bcrypt` | Already an indirect dep (via gin/validator); no new framework |
| SSH client | `golang.org/x/crypto/ssh` | Same package as bcrypt; pure Go, no CGO. Known per-channel throughput ceiling — see §11.1 |
| YAML config | `gopkg.in/yaml.v3` | Already an indirect dep; used for `config.yml`/`users.yml`/`hosts.yml` — hand-editable by design, not a config framework |
| Web sessions | Server-side random tokens in an in-memory store, `HttpOnly` cookie | Not JWT — no persistence needed, a restart logging everyone out is acceptable (§11) |
| Logging | stdlib `log/slog` (JSON handler) | No dependency needed |
| Config | CLI flags via stdlib `flag`, with env-var fallbacks, plus `config.yml`/`users.yml`/`hosts.yml` | See §9 |
| Frontend embedding | `embed.FS` (`//go:embed`) | Single-binary distribution |

Do **not** add GORM, sqlc, zap, viper, socket.io, or an E2E test framework.

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
Browser (xterm.js) ⇄ WebSocket ⇄ Go backend ⇄ session.Backend ⇄ local shell (PTY)
     (session cookie)            (auth-gated)  ⇄ session.Backend ⇄ remote shell (SSH)
                     REST (JSON) ⇅
                    SQLite (session metadata) + config.yml/users.yml/hosts.yml
```

Every request — REST and WS alike — carries the browser's session cookie and
is scoped to the authenticated user (§10's auth model, §6). `session.Backend`
(§4.2) is the abstraction that lets one `Manager` drive both a local PTY and
an SSH-backed remote shell through identical read/broadcast/scrollback code.

**Core invariant:** the PTY (or SSH session) lives in the backend process.
Browser connections are ephemeral views onto it. Killing every browser tab
must not affect the shell.

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
      middleware.go         # requireAuth, requireAdmin
      auth.go                # bootstrap/login/logout/me, admin config
      admin_users.go          # admin user list/delete/promote
      hosts.go                 # per-user host CRUD
      hostkeys.go                # host-key probe/trust
      sessions.go                 # REST handlers (local + SSH)
      directories.go
      errors.go                   # unified error responses
    ws/                     # WebSocket endpoint + client pumps
      handler.go
      client.go             # read pump / write pump per client
      events.go             # /ws/events: session state fan-out (see §5)
      protocol.go           # message types (see §5)
    session/
      manager.go            # Manager (the core component; local + SSH)
      session.go             # Session struct + lifecycle
      backend.go               # Backend interface (§4.2) — local PTY / SSH
      ringbuffer.go
      replay.go              # strips terminal queries from a snapshot (§8)
      foreground.go           # foreground lookup + sampler (§4.7)
      title.go                # OSC 0/2 window-title scanner (§4.8)
      events.go                # subscriber fan-out for /ws/events
    terminal/
      pty.go                # local PTY start/resize/kill; implements Backend
      foreground_linux.go   # TIOCGPGRP + /proc lookup (§4.7)
      foreground_other.go   # build fallback: reports nothing
    sshpty/
      sshpty.go              # SSH-backed Backend implementation
      hostkey.go               # TOFU HostKeyCallback, ProbeHostKey
      exchange.go                # ExchangeKeys — passwordless setup
    hostops/                # process tree + file browser/transfer (§4.10)
      hostops.go              # HostSession, Transport, FileTransport, Platform
      transport_local.go        # localTransport — stdlib os/exec + os.*
      transport_ssh.go            # sshTransport — reuses the session's *ssh.Client
      platform_linux.go             # ps-based ProcessTree
      platform_windows.go             # PowerShell-based ProcessTree
      ops.go                            # Delete/Copy progress tracking (§5.2)
    auth/
      users.go               # users.yml store, bcrypt
      sessions.go              # in-memory web session store (sliding TTL)
    hosts/
      hosts.go                # per-user hosts.yml store
    serverconfig/
      serverconfig.go          # config.yml store
    storage/
      sqlite.go             # open + migrate
      sessions.go            # CRUD queries (now user/target scoped)
    config/
      config.go             # CLI flags (--data-dir, --shells, …)
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

type TargetType string

const (
    TargetLocal TargetType = "local"
    TargetSSH   TargetType = "ssh"
)

type Session struct {
    ID           string    // UUID v4
    Name         string
    UserID       string     // owner — every lookup is scoped to this (§4.5, §10)
    TargetType   TargetType // "local" | "ssh"
    Directory    string    // relative to root, e.g. "project-a" (local only)
    Shell        string    // "bash" | "zsh" | "fish" (local only)
    HostID       string     // the owning user's host id (ssh only)
    HostDisplayName string  // snapshotted host name at creation (ssh only) —
                             // survives the host being renamed or deleted
    Status       Status
    PID          int
    Created      time.Time
    LastActivity time.Time
    Rows, Cols   uint16

    // runtime-only (never persisted)
    backend Backend // local PTY or SSH-backed shell — see below
    buffer  *RingBuffer
    clients map[*Client]struct{}
    mu      sync.Mutex
}
```

**`Backend`** (`internal/session/backend.go`) is what lets one `Manager` drive
both target types through identical `readLoop`/ring-buffer/broadcast/
scrollback/terminate code — neither of those has any idea whether it's
talking to a local PTY or a remote shell:

```go
type Backend interface {
    Read(p []byte) (int, error)
    Write(data []byte) error
    Resize(rows, cols uint16) error
    Pid() int                  // 0 for SSH — no local meaning
    Signal(sig syscall.Signal) // best-effort for SSH; many sshd reject it
    Wait()
    CloseFile()
    Foreground() terminal.Foreground // always zero-value for SSH — §4.7
}
```

`*terminal.PTY` (local) and `*sshpty.PTY` (SSH, `internal/sshpty`) both
implement it. Nothing above `Backend` — `Manager`, `Session`, `readLoop`, the
WS layer — branches on target type; only `Manager.CreateLocal` /
`Manager.CreateSSH` construct the right one.

### 4.3 Manager responsibilities
- `CreateLocal(userID, name, dir, shell) (Info, error)` — validates dir
  (§4.5) and shell (must be in allowlist and exist on PATH), starts a local
  PTY, spawns the read-broadcast goroutine, persists metadata stamped with
  `UserID`/`TargetType:"local"`, returns a snapshot.
- `CreateSSH(userID, name, hostID, hostDisplayName, target) (Info, error)` —
  dials the host via `internal/sshpty` (host-key TOFU checked first, §4.5.1);
  on an untrusted/changed host key, returns the connection's
  `ErrHostKeyUnknown`/`ErrHostKeyChanged` unwrapped and creates **no**
  session or DB row — the caller retries after the user trusts the key
  (§6). On success, otherwise identical to `CreateLocal`.
- `Get(id, userID)`, `List(userID)` — from in-memory map, merged with
  `stopped` rows from SQLite that are no longer in memory after restart.
  **Every call is scoped to `userID`**: a session owned by someone else is
  reported exactly like a session that doesn't exist (`ErrNotFound`), never
  a distinct "forbidden" — an id probe for a session you don't own learns
  nothing (§10).
- `Delete(id, userID)` — SIGTERM the process group (local) or best-effort
  SSH signal + force-close (remote), 5 s grace, then SIGKILL/force-close;
  disconnect clients with an `exit` control message; delete DB row.
- `Restart(id, userID)` — local: identical dir/shell revalidation as
  `CreateLocal`. SSH: re-resolves the *current* host config (including its
  current pinned host-key fingerprint) through a `HostResolver`; a deleted
  host fails with `ErrHostNotFound`, a changed host key surfaces the same
  `host_key_changed` response a fresh connect would. SSH credentials are
  never persisted to SQLite, so this re-fetch is required on every restart.
- `Attach(id, userID, client)` / `Detach(id, client)`.
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
Root is the local-host workspace, `--workspace-dir` (default
`<data-dir>/workspace`, §9), only reachable when an admin has enabled
`allowLocalHost` in `config.yml` (§9, §10). It is **one shared root for
every permitted user** — not a per-user root — matching the pre-multi-user
behavior exactly. For any user-supplied directory:
1. Reject empty, absolute paths, and any path containing `..` segments.
2. `full := filepath.Join(root, filepath.Clean(userPath))`
3. `resolved, err := filepath.EvalSymlinks(full)` — must succeed.
4. Require `resolved == root` or `strings.HasPrefix(resolved, rootResolved + string(os.PathSeparator))`
   where `rootResolved = filepath.EvalSymlinks(root)`.
5. Must be an existing directory.
Write unit tests for: `..`, `../..`, absolute paths, symlinks pointing
outside root, and valid nested paths.

**The same "never trust the caller" discipline applies to ownership.** Every
session and host lookup is scoped to the authenticated user id taken from the
session cookie (§10) — never one supplied by the client in a request body or
query string. `Manager`'s `userID`-scoped methods (§4.3) are this rule's
session-side instance; `internal/hosts.Store` being opened strictly by
`c.MustGet("userID")` (§6) is its host-side instance.

### 4.5.1 SSH host-key trust (security-critical)
`internal/sshpty` never connects to a host whose key it hasn't been told to
trust, and never silently accepts a key that changed:
- Each host in `hosts.yml` (§9) pins the fingerprint (`ssh.FingerprintSHA256`)
  of the key it last connected with. Empty means "not yet trusted."
- The `ssh.ClientConfig.HostKeyCallback` compares the presented key's
  fingerprint against the pin: no pin → `ErrHostKeyUnknown`; pin present but
  mismatched → `ErrHostKeyChanged`; match → proceed. **Never**
  `ssh.InsecureIgnoreHostKey()`.
- Both errors abort the dial before any session is created and are
  propagated, typed, up through `Manager.CreateSSH`/`Restart` to the API
  layer (§6) as a 409 the frontend turns into an explicit "trust this key?"
  prompt (§7) — the user must act before a connection is made.
- Trusting a key (`POST /api/hosts/:id/host-key/trust`) re-probes the host
  server-side before writing the pin, so a stale or client-forged fingerprint
  can't be persisted.
- The same TOFU check gates `ExchangeKeys` (§4.5.2) — passwordless setup is
  not a bypass.

### 4.5.2 SSH key exchange (passwordless login setup)
`internal/sshpty.ExchangeKeys(target, username, password)` is the backend for
the Hosts page's "Exchange SSH keys" action: generate an ed25519 keypair,
dial the host with the supplied password (subject to §4.5.1's TOFU check),
run one idempotent remote command that appends the new public key to
`~/.ssh/authorized_keys`, then return the generated key material so the
caller can switch that host to `authMethod:"privateKey"`. **The password is
used for exactly that one dial and is never written to disk, to `hosts.yml`,
or logged** — enforced at the API layer too: `POST /api/hosts/:id/exchange-keys`
clears any stored `Password` on that host regardless of what the client
sends, once the exchange succeeds.

### 4.6 Session lifecycle & housekeeping
- `LastActivity` updated on any PTY read or client input (throttle DB writes
  to at most once per 30 s).
- No automatic session killing in v0.1 (persistence is the point). Optional
  `--idle-timeout` flag may be added later; default off.
- Graceful shutdown: on SIGTERM, mark all running sessions `stopped` in DB,
  SIGTERM child process groups, close WS connections, then exit.
- Start shells with `Setsid`/process group so `Delete` can kill the whole tree.

### 4.7 Session foreground (app-agnostic, no emulation)

**Local sessions only.** This entire section applies to `TargetLocal`
sessions. An SSH-backed session's `Backend.Foreground()` always returns the
zero value — there is no way to introspect a remote process's `/proc` from
here, and `sampleSession`'s existing diff logic already treats the zero
value as "nothing to report," so an SSH session's dashboard card simply
shows no foreground program. No code in this section changes for SSH.

A running session reports the name of the program in its foreground and that
program's working directory. Both are facts read from the kernel and passed
through unchanged. Nothing here knows about any particular program: adding
support for a new TUI means adding no code.

This is the half of tmux's design we keep. tmux resolves
`#{pane_current_command}` with `tcgetpgrp()` on the pty master plus a `/proc`
lookup (`osdep-linux.c`) and shows the answer; what that program *wants* is left
to the person reading. We do the same and stop there — see §14.2.

**The lookup** — `ioctl(TIOCGPGRP)` on the pty **master**, then
`/proc/<pgid>/comm` and `/proc/<pgid>/cwd`. Exact, not inferred: the kernel is
the authority on which program owns the terminal. Sampled once a second, not
per byte.

**The group leader is not always the program.** A shell running a script does no
job control, so `bash deploy.sh` leaves the `ping` it starts in the script's own
process group: `TIOCGPGRP` can only name the leader, and the program the user is
waiting on sits one level below it. So the lookup descends from the leader
through `/proc/<pid>/task/<pid>/children`, and the chain it collects —
`bash › ping` — is what the UI shows.

Only children **still in the group** are followed, and that condition is the
whole safety of it: an interactive shell puts every job in a group of its own,
so a backgrounded `sleep 300 &` has a different group and is skipped. Without
the check, a shell sitting at its prompt with anything in the background would
be reported as running it. Where a pipeline inside a script puts several
children in the group at once, the one that started first wins — it is what the
line is about, and it is also the answer tmux gives for the same pipeline typed
at a prompt.

The descent stops at eight levels, and only the leader's own thread is asked for
children: a shell is single threaded, and scanning every thread would cost a
directory read per sample to cover a case that does not arise. A kernel without
`CONFIG_PROC_CHILDREN` yields a one-link chain, which is exactly the behaviour
that came before it.

Take the fd through `File.SyscallConn()`, never `File.Fd()` — `Fd()` pulls the
file out of the runtime poller and switches it to blocking mode, which would
quietly break the read loop and `CloseFile()`.

`cwd` is a path the user did not supply but the UI displays, so it goes through
the §4.5 sandbox like any other: made relative to root, dropped if it resolves
outside. A session whose shell has `cd`-ed out of root shows its stored
`directory` instead.

The label keeps the chain's ends when it runs deeper than three links —
`bash › … › cc1plus`. The outermost says what was started, the innermost what is
running, and everything between them is scaffolding a build system put there.

**Known limits, accepted by design.** Off Linux there is no `/proc`, so
`command` and `cwd` are empty. Behind a wrapper with a pty of its own — `tmux`,
`docker run -it`, an ssh hop — the lookup stops at our own pty and names the
wrapper, not the shell inside it. `sudo -i` and `su` lead the group themselves,
and whether the chain reaches the shell underneath depends on whether they keep
it in their own process group (unverified; neither is installed in the container
these measurements were taken in).

**Cost.** One manager-wide goroutine ticking every second, not one per session:
per session per sample, an ioctl and two small `/proc` reads.

**What is deliberately *not* here: a derived activity state.** An earlier
version of this section classified each session as `busy`, `waiting` or `idle`
from the foreground kind, output cadence, bracketed-paste mode, BEL and OSC 133
prompt marks, so the dashboard could say which session wanted an answer. It
worked well enough to ship and was removed again before a release: the one state
worth having — "this session is waiting for you" — is the one the terminal
cannot establish. A program at a prompt, mid-question, and halfway through a
quiet build are the same bytes from outside the pty, and every discrimination
tried held for the programs it was measured against and failed for the next one.
A wrong `waiting` is worse than none: it trains you to ignore the indicator.

The whole of it — classifier, escape scanner, prompt marks, the measured tables
behind the thresholds, and the `cmd/ptycapture` tool that produced them — is
kept on the `park/session-activity` branch rather than in the history alone.
Anything here that reads as a leftover of it (the foreground *chain*, which only
the classifier needed to resolve a nested shell) is kept because the label is
better for it.

Re-landing it means reverting the removal commit and merging that branch. Do not
rebuild it from scratch; do re-run the capture before trusting any of its
numbers.

---

### 4.8 Window title (OSC 0/2)

A running session also reports the window title the program inside it set. That
is the sequence every terminal has implemented since xterm — `ESC ] 0 ; text
BEL` sets the icon name and the title, `ESC ] 2 ; text ST` the title alone — and
nothing about it is Windows-specific: bash and zsh emit it from
`PROMPT_COMMAND` / `precmd`, vim from `'titlestring'`, and long-running tools
use it to say what they are working on.

**It is the other half of §4.7, and the weaker half.** `command` is read from
the kernel and a program cannot be wrong about it. The title is that program's
own account of itself: usually the better line to read — it says what `claude`
is doing rather than merely that `claude` is running — but a claim rather than a
fact, and one that goes stale, because a program that exits without restoring
the title leaves its last one standing until the shell writes the next prompt.
So both are reported and the foreground stays the headline. tmux draws the same
line: it keeps the title in `pane_title`, but `allow-rename` is off by default
and the window is named from `pane_current_command`.

It is also passed through as the program wrote it, which `cwd` is not: a default
bash prompt hook puts `\u@\h: \w` in the title, so the dashboard shows an
absolute server path there while the line above it stays relative to root. That
is the program's text, and rewriting it would be guessing at what it meant.

**Where it is read.** `titleScanner` in `internal/session/title.go`, driven by
the read loop over the same bytes it broadcasts. A state machine rather than a
per-chunk search, because a PTY read ends wherever the kernel filled the buffer:
`ESC ] 0 ;` in one chunk and the title in the next is routine. Only string
sequences need a state of their own — a CSI cannot hide an ESC between its
introducer and its final byte, and neither can UTF-8.

**Cost, and why this stays inside §14.2.** The read loop is the hot path and
had no byte inspection on it at all, so the fast path is the feature: a chunk
with no ESC in it and no sequence still open is rejected by a single
`IndexByte`, which is what ordinary output — a build log, a `cat` — always is.
Past that the scanner reads one string out of one sequence. It builds no grid,
tracks no cursor and answers nothing, exactly like `sanitizeReplay` and
`endsInAltScreen` (§8) and for the same reason: an escape sequence is the only
thing in a PTY stream that is not text.

**Publishing is left to the §4.7 sampler.** The read loop stores the title and
sets a flag; the once-a-second sampler carries it out with the foreground. That
costs a second of latency on a label and buys the bound that matters — a program
repainting its title inside a progress loop can move the event fan-out no faster
than once per session per second.

**What is dropped.** OSC 1, which sets the icon name — the label for a minimised
window, not the title; xterm.js's `onTitleChange` ignores it too, and a session
should read the same here as in a terminal beside it. Control characters and
invalid UTF-8, neither of which is a line in a list. Everything past 256 payload
bytes, the sequence itself kept: what a program put in the first 256 is still
what it calls itself. An empty payload is a program clearing its title and is
passed on as such, and a stopped session's title is cleared with the rest of its
derived state — no shell is left to reach another prompt and overwrite it.

### 4.9 Terminal modes on attach

A terminal carries state that is not on its screen: whether the alternate
screen is up, whether a program asked for mouse reports and in which encoding,
whether the line editor is given bracketed paste, what the arrow keys send. A
program switches these on for itself when it starts and off again when it
exits, with a DEC private mode set or reset.

**The ring buffer cannot carry them, and that is a bug rather than a detail.**
The buffer is content, and it is bounded. A program that repaints — htop, vim,
a build with a progress bar — pushes its own opening `ESC [ ? 1049 h ESC [ ?
1002 h` off the front of it long before anyone switches sessions, so what a
later attach replays is a stream of repaints that switches nothing on. The
frontend builds a fresh xterm for every session switch (§7 keys `TerminalView`
on the session id), and that terminal then sits on the normal screen with mouse
reporting off while the program on the other end believes both are on.
Symptom, as reported in #92: scrolling a TUI does nothing, reopening the tab
replays the same truncated snapshot and does not help, and only resizing the
window brings it back — SIGWINCH makes the program redraw and re-issue its own
setup.

**The fix is a preamble, not an emulator.** `modeScanner`
(`internal/session/modes.go`) walks the live stream from the read loop, over
the same bytes it broadcasts and beside `titleScanner`, and keeps a handful of
values: the alternate screen (47/1047/1049, the spelling the program used), the
mouse tracking mode (1000/1002/1003) and its encoding (1005/1006/1015/1016),
bracketed paste (2004), focus reporting (1004), application cursor keys
(DECCKM) and keypad (DECKPAM/DECKPNM), and the two that are on in a reset
terminal — the cursor (25) and autowrap (7). `attach` renders them as the
sequences that put a fresh terminal into that state and writes them **ahead of**
the replay, so anything the replay still carries is applied afterwards and wins:
a snapshot that survived intact is unaffected. `replayBytes` counts both, which
is what §5 asks of it. A session in a reset terminal — an ordinary shell —
produces no preamble and no bytes.

**tmux does not have this problem** because its state does not live in the
stream: it parses output into a screen model with a mode bitset beside it, and
writes those modes out to whichever client attaches. This is that half of tmux's
design and only that half. The character grid stays out, per §14.2 — nothing
here holds a cell, a cursor position or an attribute.

**Where it stops.** The mouse modes are kept as one tracking value and one
encoding rather than a bit each, because that is how xterm.js models them: it
keeps one active protocol and one active encoding, so a reset of any of the
three tracking modes turns tracking off. The scroll region (DECSTBM) is
deliberately not tracked: it is two numbers rather than a flag, and re-issuing
it homes the cursor — the same trap `restoreSeparator` documents. Modes the
browser terminal owns itself or does not implement are ignored rather than
guessed at. A restart builds a new session and a new read loop, so no mode
outlives the shell that set it.

### 4.10 Host operations: process tree, file browser, transfer

A session's target — local or SSH, Linux or Windows — also exposes a small,
**fixed set of named operations**: its process tree, listing/moving/copying/
deleting files, and downloading/uploading one file to/from the browser. This
is deliberately not a generic remote-command facility (§1, CLAUDE.md's
"Scope" rule) — every operation is a specific typed method with typed
arguments, never a caller-supplied command line.

**Two independent axes, not a class per combination.** *Transport* is how a
command or file operation reaches the target: `local` (this process) or `ssh`
(the session's own already-dialed, TOFU-verified `*ssh.Client` — §4.5.1 — no
second connection, no second trust decision). *Platform* is what the target
looks like: Linux, Windows, or an unsupported "other". They compose:

```go
// HostSession is the top-level handle a session's hostops hang off. One is
// built per session, alongside its Backend (§4.2), and lives exactly as
// long as it does — session.Session gets a HostOps() accessor returning it.
type HostSession struct {
    transport Transport
    platform  Platform // consulted only by ProcessTree — see below
}

// Transport moves bytes/commands to and from the target. Local and SSH are
// its only two implementations, ever — a third transport (say, WinRM) would
// be a new Transport, not a new type per OS it happens to support.
type Transport interface {
    Exec(ctx context.Context, line string) (Result, error)
    Files() FileTransport
}

// FileTransport is OS-agnostic by construction. Local's is a thin wrapper
// over os.ReadDir/os.ReadFile/os.WriteFile/os.Rename; SSH's is
// github.com/pkg/sftp over the session's ssh.Client — the sftp-server
// subsystem is what OpenSSH-for-Windows serves too, so this one
// implementation covers every SSH target OS with no per-platform branch.
type FileTransport interface {
    List(ctx context.Context, path string) ([]DirEntry, error)
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    Rename(ctx context.Context, oldpath, newpath string) error // Move
    Remove(ctx context.Context, path string) error             // walks + deletes for a directory — neither SFTP nor a single syscall has a recursive-delete primitive, so both transports hand-roll the same walk
    Copy(ctx context.Context, src, dst string) error            // Read(src) then Write(dst); whole file in memory — fine at the sizes this feature targets, revisit if it grows into bulk transfer
}

// Platform is the one thing that is genuinely OS-shaped: process listing.
// Everything else a HostSession does is transport-level and does not need
// this — a file browser and download/upload work against an SSH target
// with zero Platform support at all. Only ProcessTree needs one written for
// a given OS to work; Linux ships first, Windows second (§12c), and a
// target with neither returns a clear "unsupported platform" error rather
// than a guess.
type Platform interface {
    ProcessTree(ctx context.Context, t Transport, rootPID int) ([]Process, error)
}

type Process struct {
    PID, PPID int
    Command   string
    Children  []Process // pre-assembled tree; callers never re-link a flat list
}
```

`localTransport`/`sshTransport` implement `Transport`; `linuxPlatform`/
`windowsPlatform` implement `Platform`. `Manager.CreateSSH` builds the
`sshTransport` from the same `*ssh.Client` `sshpty.Start` already produced —
`sshpty.PTY` gains a `Client() *ssh.Client` accessor for exactly this, so
nothing dials twice. `TargetOS` (already tracked per host, §9) selects the
`Platform`; local sessions always get `linuxPlatform` — the server binary
only ships for Linux (§2), so there is no "local Windows" case to support.

**`Exec` stays internal.** No handler, route, or frontend call ever supplies
its `line` argument — `Platform` implementations build it from a fixed
template plus quoted path arguments (`ps -eo pid,ppid,comm --no-headers` for
Linux; a small `Get-CimInstance Win32_Process | Select …` for Windows). It
sits at the same trust depth `internal/sshpty` already has: the operator's
own already-authenticated connection to their own host, one more channel on
it rather than a new one.

**Long-running operations report progress; fast ones don't.** `ListDir`,
`ProcessTree`, and `Rename`/Move are single round-trips and stay synchronous
REST calls. `Delete` (a recursive walk with no natural client-visible byte
stream to hang a progress bar on) and same-target `Copy` (potentially a
large single file, same reason) are started, return an operation id
immediately, and report progress on `/ws/events` (§5.2) the same way
foreground/title changes already do — an *event*, not a poll. `Download` and
`Upload` need **no** custom progress channel at all: they are plain streamed
HTTP request/response bodies, and the browser's own `fetch`
download/upload-progress events already give the UI what it needs from the
transfer itself — building a second, server-pushed progress channel for
those would just be tracking a number the browser already has.

**Trust boundary.** Local paths still go through §4.5's sandbox — nothing
here loosens that. Remote (SSH) paths do not: the user already has a full
interactive shell on that host through the terminal this `HostSession`
belongs to, so there is no meaningful sandbox left to add beyond the
existing per-user host ownership check (§4.3, §4.5) that gates which
session's `HostSession` a request can even reach.

---

## 5. WebSocket Protocol (exact spec)

Endpoint: `GET /ws/sessions/:id` (upgraded), gated by `requireAuth` (§10)
exactly like any other route — the browser attaches the session cookie to
the upgrade handshake automatically, since it's a same-origin HTTP request
before it's a WS request; no token-in-query-string plumbing is needed, in
dev (Vite proxy) or prod alike. `mgr.Attach(id, userID, client)` folds
ownership checking into the same "session not found" close path a missing
id already used (§4.3) — attaching to a session you don't own is
indistinguishable from attaching to one that doesn't exist. Connecting to a
`stopped` session is rejected with WS close code 4404 + reason.

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
  mode preamble (§4.9) followed by the buffer with the terminal queries filtered
  out, and `replayBytes` counts what is actually sent — see §8, "Replayed output
  must not ask questions".
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
A `session` message is published on foreground or title change (§4.7, §4.8),
status change, create, rename and restart; `sessionGone` on delete. Like
`/ws/sessions/:id`, this endpoint is `requireAuth`-gated and the
snapshot/incremental messages carry only the connecting user's own sessions
(§4.3, §10) — never another user's, admin included.

This channel replaces the 5 s list poll of §12 M4. The frontend keeps polling
as a **fallback while the socket is down** — the socket closing is itself the
signal that the list may be stale, and an unreachable backend still has to mark
every session stopped (`markAllStopped`).

### 5.2 Host operation progress

`Delete` and same-target `Copy` (§4.10) are the only two hostops that report
progress, and they report it on this same `/ws/events` channel — one more
message type, not a new socket:

```json
{"type":"hostopStarted","sessionId":"…","opId":"…","kind":"delete","path":"…"}
{"type":"hostopProgress","sessionId":"…","opId":"…","done":12,"total":47}
{"type":"hostopDone","sessionId":"…","opId":"…","status":"ok","message":""}
{"type":"hostopDone","sessionId":"…","opId":"…","status":"error","message":"permission denied: …"}
```

`total` is 0/omitted while still being discovered — `Delete` counts entries as
its walk finds them, not from an up-front pass, so an early `hostopProgress`
may report `done` climbing with no `total` yet; `Copy`'s `total` is the
source file's size, known before the first byte moves. `done`/`total` count
files for `Delete`, bytes for `Copy` — the two are not comparable and no
caller should treat them as the same unit.

`POST .../hostops/delete` and `POST .../hostops/copy` (§6) return
`202 {"opId":"…"}` immediately rather than blocking; `GET
.../hostops/ops/:opId` is the REST poll fallback for the same reason §5.1's
list poll exists — the socket being down should not mean the UI has no way
to find out whether a delete finished.

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

All routes except `/api/health` and `/api/auth/*` require a valid session
cookie (§10); every handler resolves `userID := c.MustGet("userID")` and
never trusts a client-supplied user id.

| Method & Path | Purpose | Notes |
|---|---|---|
| `GET /api/health` | `{"status":"ok"}` | Public, for Docker healthcheck |
| `GET /api/auth/status` | Bootstrap/login page state | Public. `{needsSetup, allowRegistration, displayName, version}` |
| `POST /api/auth/bootstrap` | Create the first (admin) user | Public, only while `needsSetup`. `{username,password}` → sets session cookie. 409 once a user exists |
| `POST /api/auth/register` | Self-service signup | Public, only if `allowRegistration && !needsSetup`. Same body; auto-logs in. 403 if disabled |
| `POST /api/auth/login` | Log in | Public. `{username,password}` → sets cookie. 401 generic message on failure |
| `POST /api/auth/logout` | Log out | 204; revokes the session token |
| `GET /api/auth/me` | Current user | `{id,username,isAdmin}` |
| `GET /api/admin/config` / `PUT` | Server config | Admin only. `serverconfig.Config` (§9) |
| `GET /api/admin/users` | List users | Admin only. No password hashes |
| `DELETE /api/admin/users/:id` | Remove a user | Admin only. 409 `conflict` if `id` is the last admin |
| `PATCH /api/admin/users/:id` | Promote/demote (`{"isAdmin":bool}`) | Admin only. Same 409 guard |
| `GET /api/hosts` | List the caller's hosts | Secrets masked (`hasPassword`/`hasPrivateKey`); host-key fingerprint fields included (not secret) |
| `POST /api/hosts` | Create a host | Body: `Host` fields (§9) minus id/timestamps |
| `GET /api/hosts/:id` | Get one host | Owner-scoped; 404 if not the caller's |
| `PUT /api/hosts/:id` | Update a host | Omitted secret fields mean "leave unchanged"; changing `authMethod` drops the other method's secret |
| `DELETE /api/hosts/:id` | Delete a host | Sessions that already snapshotted its display name are unaffected; a later restart fails cleanly with "host not found" |
| `POST /api/hosts/:id/host-key/probe` | Check the host's current key | `{keyType,fingerprint,status:"new"\|"unchanged"\|"changed",previousFingerprint?}`. No session created |
| `POST /api/hosts/:id/host-key/trust` | Pin a host key | `{fingerprint,keyType}` → re-probes server-side before writing, TOCTOU-safe |
| `POST /api/hosts/:id/exchange-keys` | Passwordless login setup (§4.5.2) | `{username,password}`, password used once, never stored. Same host-key 409s as session creation |
| `GET /api/sessions` | List the caller's sessions | Includes `clientCount` per session; strictly owner-scoped, admins included (§4.3) |
| `POST /api/sessions` | Create | Body below; 201 + session JSON. 409 `host_key_unverified`/`host_key_changed` for SSH targets whose key needs trusting first (§4.5.1) |
| `GET /api/sessions/:id` | Get one | Owner-scoped |
| `DELETE /api/sessions/:id` | Kill + remove permanently | 204; also drops the session's scrollback snapshot and history file (§8) |
| `PATCH /api/sessions/:id` | Rename (`{"name":"…"}`) | |
| `POST /api/sessions/:id/restart` | Give a stopped session a new shell under the same id | No body; 200 + session JSON. 404 unknown, 409 still running, 400 if the directory or shell no longer validates (local), 404 `host_not_found` or 409 host-key responses (SSH) |
| `GET /api/directories` | Browse dirs under the local-host workspace; optional `?path=` (relative, validated by §4.5) navigates into subdirs | 403 if `allowLocalHost` is off. `{"path":"project-a","parent":".","directories":["nested", …]}` — `path` is the cleaned listed path (`.`=root), `parent` is `null` at root |
| `GET /api/config` | Display name, available shells, `allowLocalHost`, version | Shells = allowlist ∩ installed |
| `GET /api/sessions/:id/hostops/process-tree` | Process tree (§4.10) | `{"rootPid":123,"processes":[…Process…]}`. 501 `unsupported_platform` if the target's `Platform` has no `ProcessTree` |
| `GET /api/sessions/:id/hostops/files?path=` | List a directory on the target | `{"path":"…","entries":[…DirEntry…]}` |
| `POST /api/sessions/:id/hostops/move` | Move/rename on the target | `{"src":"…","dst":"…"}` → 200, synchronous |
| `POST /api/sessions/:id/hostops/copy` | Copy on the target | `{"src":"…","dst":"…"}` → 202 `{"opId":"…"}`, progress on §5.2 |
| `DELETE /api/sessions/:id/hostops/files?path=` | Delete a file or directory on the target | 202 `{"opId":"…"}`, progress on §5.2 |
| `GET /api/sessions/:id/hostops/ops/:opId` | Poll a `delete`/`copy` op | `{"opId":"…","kind":"delete","done":12,"total":47,"status":"running"}`. Poll fallback for §5.2 |
| `GET /api/sessions/:id/hostops/download?path=` | Download one file from the target | Streamed body, `Content-Disposition: attachment`, `Content-Length` when known |
| `POST /api/sessions/:id/hostops/upload?path=` | Upload one file to the target | Raw streamed body, not multipart — goes straight into `FileTransport.Write`. Its own, much larger body-size ceiling (§11) — not the 32 KiB JSON-endpoint cap |

Every `hostops` route resolves the same `mgr.Get(id, userID)` every other
`/api/sessions/:id/*` route does (§4.3) before reaching the session's
`HostSession` — a hostops request against a session you don't own is
indistinguishable from one against a session that doesn't exist, same as
everywhere else. `DirEntry` is `{"name":"…","isDir":bool,"size":123,"modTime":"…"}`
(RFC 3339, §9); `Process` is `{"pid":1,"ppid":0,"command":"…","children":[…]}`.

Create-session body is a discriminator on `target`:
```json
{"name":"Backend","target":"local","directory":"project-a","shell":"bash"}
{"name":"prod-db","target":"ssh","hostId":"…"}
```
Validation: name 1–64 chars; `target:"local"` requires `allowLocalHost` on,
directory passes §4.5, shell in allowlist; `target:"ssh"` requires `hostId`
to resolve to one of the caller's own hosts.

Session JSON shape (single source of truth — mirror in TS types):
```json
{
  "id":"…","name":"Backend",
  "targetType":"local","directory":"project-a","shell":"bash",
  "hostId":null,"hostDisplayName":null,
  "status":"running","pid":12345,
  "created":"2026-07-16T12:00:00Z","lastActivity":"2026-07-16T12:34:56Z",
  "rows":32,"cols":120,"clientCount":2,
  "command":"claude","cwd":"project-a/backend","title":"claude — sessile"
}
```
An SSH session instead carries `"targetType":"ssh","directory":null,
"shell":null,"hostId":"…","hostDisplayName":"prod-db"` and `pid` is always
`0` (§4.2's `Backend.Pid()`).

The shape is defined once, in Go, as `session.JSON` with `session.ToJSON(Info)`
— not in `internal/api`. Both the REST handlers and the event channel (§5.1)
serialise it, and `api` imports `ws`, so a shape owned by `api` could not be
reached from either of the packages that push it. `session` already owns the
control-message types for the same reason.

The last three fields come from §4.7 and §4.8 and are runtime-only — none of
them is persisted, and the SQLite schema (§8) is unchanged:

| Field | Meaning |
|---|---|
| `command` | foreground program, e.g. `claude`, `htop`, or the shell when at a prompt. A script and what it started are joined with ` › ` — `bash › ping` — and a chain past three links keeps its ends: `bash › … › cc1plus`. Empty where unavailable |
| `cwd` | the shell's actual working directory, relative to root — follows `cd`, unlike `directory`. Empty when unknown or outside root |
| `title` | the window title the program set for itself with OSC 0/2 (§4.8), e.g. `~/project` from a shell's prompt hook. Empty until something sets one |

All three are empty for a stopped session: a dead session must not keep
advertising the program it was running when it died, nor the title that program
left behind.

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
                # auth.ts: current user, login/logout/bootstrap/register
                # hosts.ts: per-user host CRUD, host-key probe/trust
                # admin.ts: admin user list/delete/promote
  components/   # Sidebar.vue, SessionListItem.vue, NewSessionDialog.vue,
                # TerminalView.vue, StatusDot.vue, TabBar.vue,
                # HostDialog.vue, HostKeyTrustDialog.vue,
                # ProcessTreePanel.vue, FileBrowserPanel.vue (§4.10, §12c)
  pages/        # DashboardPage.vue, TerminalPage.vue, SettingsPage.vue,
                # LoginPage.vue, HostsPage.vue, UsersPage.vue (admin-only)
  router/       # beforeEach guard: redirect to /login when unauthenticated,
                # redirect away from adminOnly routes when not an admin
```

### Pages
- **Login** (`/login`, public): fetches `GET /api/auth/status` on mount.
  While `needsSetup`, shows a "create the admin account" form
  (`POST /api/auth/bootstrap`); otherwise a normal login form, with a
  "Create account" link shown only when `allowRegistration` is on. The
  router's `beforeEach` guard sends any unauthenticated visit to a
  non-public route here.
- **Hosts** (`/hosts`): the caller's own SSH hosts, grouped by `group`,
  add/edit dialog (`HostDialog.vue`) for name/group/address/username/
  auth-method/password-or-key/target-OS/terminal-type, a read-only pinned
  host-key fingerprint, a "Verify host key" action, and "Exchange SSH keys"
  (§4.5.2) for passwordless setup. Both host-key actions can surface
  `HostKeyTrustDialog.vue` (§5, §6) when the presented key is unknown or has
  changed — no action silently trusts a key.
- **Users** (`/admin/users`, admin only): list of accounts with a
  promote/demote toggle and delete, surfacing the "can't remove the last
  admin" 409 inline.
- **Dashboard** (`/`): session cards, "New Session" button. A card carries
  the status indicator, name, target (local shell or the SSH host's display
  name), the foreground program for local sessions only (§4.7) with the
  session's window title (§4.8, both local and SSH) on the line below it,
  the working directory — `cwd` when known, falling back to the stored
  `directory` — the client count when more than one browser is attached, and
  last activity.

  The two middle lines are deliberately unequal: the foreground is mono and the
  brighter of the two because it is the fact, the title is dimmer because it is
  the program's own word for what it is doing. Both rows hold their height while
  empty, so a grid of cards does not reflow every time one of them changes.

  The indicator is one component (`StatusDot.vue`) used on the card, in the
  sidebar and in the tab bar. Two states, reusing the palette already in the app
  rather than introducing colours:

  | State | Rendering |
  |---|---|
  | `running` | emerald dot with glow |
  | `stopped` | slate dot |

  No browser notifications and no document-title signalling, per "zero
  distractions" (§1).
- **Terminal** (`/sessions/:id`): full-height xterm, tab bar of open
  sessions, dark theme default. A "Files" panel (`FileBrowserPanel.vue`,
  §4.10) opens alongside the terminal rather than as a separate page — it
  belongs to *this* session's `HostSession`, not to the session list, the
  same reason foreground/title (§4.7, §4.8) are per-card rather than a
  standalone view. It carries the file browser (list/move/copy/delete,
  download/upload) and, in its own tab within the panel, the process tree
  (`ProcessTreePanel.vue`) rooted at the session's own foreground PID —
  local sessions get it from §4.7's existing lookup, SSH sessions from
  §4.10's `ProcessTree`. A `Delete`/`Copy` in progress shows an inline
  progress bar fed by §5.2's events, not a poll.
- **Settings** (`/settings`): read-only server config display (display name,
  shells, version, whether local-host sessions are allowed), plus — admin
  only — an editable panel for `displayName`/`allowRegistration`/
  `allowLocalHost` (`GET`/`PUT /api/admin/config`), plus the client
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
- <640 px: bottom navigation (Dashboard / Hosts / Settings, admin gets
  Users too), full-screen terminal, horizontally scrollable tab bar,
  44 px+ touch targets.

New-session flow: single dialog, name input, then a host picker (from
`/api/hosts`, grouped) as the primary path. When `allowLocalHost` is on, a
secondary "Use this host (local)" option reveals the original directory
`<select>` (from `/api/directories`) and shell `<select>` (from
`/api/config`). Submitting an SSH target that returns
`host_key_unverified`/`host_key_changed` opens `HostKeyTrustDialog.vue`
instead of creating a session (§4.5.1, §6); accepting retries the same
create request. On success: navigate straight to the terminal page — this
part is unchanged for both target types, since `useTerminal.ts`'s
`connect(id)` only ever needs a session id (§4.2's `Backend` is what makes
that true).

---

## 8. Persistence (SQLite + YAML)

Session runtime metadata lives in SQLite, unchanged in kind from v0.1–v0.2.
Everything that is *configuration* rather than session state — server
settings, user accounts, per-user host definitions, including credentials
(decision recorded in §11) — lives in hand-editable YAML files instead, none
of it ever written into SQLite:

```
<data-dir>/                # --data-dir; /config in Docker
  config.yml                # serverconfig.Config — display name, allowRegistration,
                             # allowLocalHost (§9)
  users.yml                  # []auth.User — id, username, bcrypt hash, isAdmin
  users/<user-id>/
    hosts.yml                 # []hosts.Host — SSH targets, credentials, host-key pin
  sessions.db
  scrollback/<session-id>.bin
  history/<session-id>

<workspace-dir>/           # --workspace-dir; /workspace in Docker; defaults to
                            # <data-dir>/workspace when unset (§9) — the shared
                            # local-host sandbox root (§4.5), only used when
                            # allowLocalHost is on. Kept separate from --data-dir
                            # so the two can be backed up independently.
```

Sessions table schema (run as an embedded migration on startup; `M17`/§12b
added the last four columns to the v0.1–v0.2 table via
`ALTER TABLE ... ADD COLUMN`, guarded by a `PRAGMA table_info` check since
this was the project's first real schema migration):

```sql
CREATE TABLE IF NOT EXISTS sessions (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  user_id           TEXT NOT NULL DEFAULT '',
  target_type       TEXT NOT NULL DEFAULT 'local',
  host_id           TEXT NOT NULL DEFAULT '',
  host_display_name TEXT NOT NULL DEFAULT '',
  directory         TEXT NOT NULL,
  shell             TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'running',
  created           TEXT NOT NULL,          -- RFC 3339 UTC
  last_activity     TEXT NOT NULL
);
```
Rows created before the migration carry `user_id=''` and become invisible
under strict per-owner scoping (§4.3, §10) — an expected, one-time
consequence of adding auth to a previously single-tenant table.

Web login sessions are **not** persisted anywhere (§10) — they live only in
an in-memory token store, by design.

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
deliberately **a separate directory from `--workspace-dir`** (§4.5, §9): a
local-host shell that could read or rewrite its own history file inside the
sandbox would make the restored history worthless, and a relative path would
resolve against the *session's* directory once it reaches a shell's
environment. SSH sessions have no `HISTFILE` injected — the remote shell's
own history config applies,
unmodified; only scrollback (below) is captured for them, same as local.

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

`--root` is gone, renamed to `--workspace-dir` — same meaning (the
local-host sandbox directory), but now optional and access-gated at runtime
by `config.yml`'s `allowLocalHost` rather than being the server's only mode.
Everything else the server keeps — session DB, scrollback, history,
`config.yml`, `users.yml`, per-user `hosts.yml` — lives under
`--data-dir`, a **separate** directory from `--workspace-dir` on purpose: the
small, sensitive state in `--data-dir` (`users.yml`, `hosts.yml` with
credentials) can then be backed up, or excluded from a backup, independently
of a workspace that's only used when local-host sessions are enabled and is
never sensitive on its own.

| Flag | Env | Default |
|---|---|---|
| `--addr` | `TSM_ADDR` | `:8080` |
| `--data-dir` | `TSM_DATA_DIR` | `./data` → in Docker: `/config`. Holds `config.yml`, `users.yml`, `users/`, `sessions.db`, `scrollback/`, `history/` |
| `--workspace-dir` | `TSM_WORKSPACE_DIR` | `<data-dir>/workspace` → in Docker: `/workspace`. The local-host sandbox root, only reachable when `allowLocalHost` is on |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` (local-host allowlist only — irrelevant unless `allowLocalHost` is on) |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` (bytes) |
| `--session-retention` | `TSM_SESSION_RETENTION` | `0` (keep forever); Go duration, e.g. `720h` |
| `--log-level` | `TSM_LOG_LEVEL` | `info` |

Left unset, `--workspace-dir` defaults to a subdirectory of `--data-dir`, so
a bare `go run` or a single-volume deployment still needs only one
directory; set explicitly — Docker's default `CMD` does — the two nest under
whatever the operator points them at, e.g. a compose file mounting
`./data/config:/config` and `./data/workspace:/workspace` so both still live
under one `./data` folder on the host (§10).

A `--root` flag is now rejected with a helpful error pointing at
`--workspace-dir` instead, following the same retired-flag-with-a-helpful-
error pattern `--db`'s removal already established in `internal/config`.

`config.yml` (`internal/serverconfig`), created with hand-editable defaults
on first run, admin-editable afterward via `PUT /api/admin/config` (§6):
```yaml
displayName: ""            # shown on the login page; empty = generic title
allowRegistration: false   # self-service signup on the login page
allowLocalHost: false      # permit local-shell sessions on this server
```

`users.yml` (`internal/auth`) and each user's `users/<id>/hosts.yml`
(`internal/hosts`) are the other two hand-editable YAML files — see §8 for
layout and §11 for what they do and don't encrypt.

---

## 10. Dev & Build Workflow

- `backend`: `go run ./cmd/server --data-dir=$(pwd)/../sandbox/data --dev`.
  First run is "unlocked" — visiting the dev frontend's `/login` shows the
  admin-bootstrap form, not a flag or seeded credential.
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
1. `node:22-alpine` (`--platform=$BUILDPLATFORM`) → build frontend
2. `golang:1.25-alpine` (`--platform=$BUILDPLATFORM`) → copy `dist` in,
   `CGO_ENABLED=0 go build`, cross-compiled, version via `-ldflags`
3. Runtime, two variants sharing everything below: `runtime-alpine`
   (default, musl/BusyBox) and `runtime-ubuntu` (glibc, for anyone linking
   glibc-only programs into their local-host shells) — both need `bash`
   installed for shells; scratch won't work since sessions need real
   shells. Single binary copied in.
   `EXPOSE 8080`; **two volumes**, `/config` (holds `config.yml`,
   `users.yml`, `users/`, `sessions.db`, `scrollback/`, `history/` — §8/§9)
   and `/workspace` (the local-host sandbox, only used when `allowLocalHost`
   is on);
   `HEALTHCHECK` hitting `/api/health`;
   `ENTRYPOINT ["tini", "--", "sessile"]`;
   default cmd: `--data-dir=/config --workspace-dir=/workspace --shells=bash`.
- `docker-compose.yml` at repo root — both volumes nest under one `./data`
  folder on the host, so there's still one thing to back up or point a
  deploy tool at, even though the container sees two mount points:
  ```yaml
  services:
    sessile:
      build: .
      ports: ["8080:8080"]
      volumes:
        - "./data/config:/config"
        - "./data/workspace:/workspace"
      restart: unless-stopped
  ```

---

## 11. Security (v0.4 baseline)

- Directory sandbox per §4.5 (tested), now paired with per-user ownership
  scoping for sessions and hosts (§4.5, §4.3, §6) — the same "never trust
  the caller" discipline applied to identity, not just paths.
- Shell allowlist — never exec a user-supplied path (local-host sessions
  only; an SSH target's shell/command is the user's own choice on their own
  host, not something this server allowlists).
- **Auth model:** username + bcrypt-hashed password (`internal/auth`),
  server-side random session tokens in an **in-memory** store with a
  **sliding 30-day TTL** (renewed on every authenticated request), delivered
  via an `HttpOnly`, `SameSite=Lax` cookie (`Secure` unless `--dev`). Not
  JWT — no persistence is needed, and a server restart logging everyone out
  is an accepted simplification. First run is "unlocked": no users exist,
  `GET /api/auth/status` reports `needsSetup`, and the first
  `POST /api/auth/bootstrap` becomes the admin. Self-service registration
  is admin-controlled (`config.yml`'s `allowRegistration`, default off).
- **CSRF:** `SameSite=Lax` plus same-origin JSON `fetch`/WS (no CORS origin
  is ever allowed for the API) keeps CSRF risk low without a token — a
  cross-site `<form>` POST can't set the JSON content-type this API
  requires, and `SameSite=Lax` withholds the cookie from cross-site
  sub-requests entirely.
- **Host credentials are plaintext in `hosts.yml`, by design, not by
  accident.** The operator is the trusted owner of their own server and
  edits this file by hand. `// TODO(security):` marks the spot for optional
  future encryption-at-rest; it is not built in v0.4 and should not be
  added without discussion (§1).
- **SSH host-key verification is trust-on-first-use with explicit user
  prompts** (§4.5.1) — never `ssh.InsecureIgnoreHostKey()`. A host's pinned
  fingerprint lives in its `hosts.yml` entry; an unknown or changed key
  blocks the connection (and creates no session) until the user explicitly
  trusts it.
- **"Exchange SSH keys" never persists the password it's given** (§4.5.2) —
  used for exactly one dial, then discarded; the endpoint clears any
  previously stored password on that host once the exchange succeeds,
  regardless of what the client sends.
- WebSocket origin check: same-origin by default, `--allow-origin` flag to
  override (needed for Vite dev — allow `http://localhost:5173` when
  `--dev` flag set).
- Body size limits on JSON endpoints (32 KiB — raised from an initial 4 KiB,
  which a pasted private key plus the rest of a host-creation body could
  exceed). `POST .../hostops/upload` (§4.10, §6) is deliberately **not**
  under this cap — a real file upload needs its own, much larger ceiling,
  applied as separate route-group middleware rather than raising the
  JSON-endpoint cap itself, which stays sized for JSON.
- Host operations (§4.10) add no new identity or path-trust model: routes are
  scoped by the same session ownership check as every other
  `/api/sessions/:id/*` route (§4.3, §6), local paths still pass §4.5's
  sandbox, and a remote (SSH) path is not sandboxed beyond that ownership
  check — the user already has an interactive shell on that host through
  the session the operation belongs to, so there is no narrower boundary to
  enforce than "this is your own session."
- Rate limiting: still deferred — not added in this pass either. Login
  brute-forcing is the main gap this leaves open; worth revisiting before a
  wider deployment than "an admin who trusts their own users."

### 11.1 Known limitation: SSH throughput on high-latency links

`golang.org/x/crypto/ssh` has a **hardcoded, unconfigurable 2 MiB
per-channel flow-control window** (`channelWindowSize = 64 *
channelMaxPacket`, unchanged even in the newest release as of this
writing). On a link with real round-trip latency, a single channel's max
throughput is roughly `window size ÷ RTT`, which caps well below the
link's actual capacity on a slow or long-haul connection — noticed when a
bandwidth-heavy command run over an SSH session measured ~30 Mbit/s
through sessile against the same host Termius reached ~150 Mbit/s on.
Confirmed this isn't sessile's own PTY/broadcast pipeline (a real
WebSocket-attached client relaying a print-heavy local session added
~4ms of overhead over a 24ms baseline — noise) and isn't a coding
inefficiency in how the SSH client is used (measured Go throughput at two
RTT tiers landed within 93-94% of the pure `window_size ÷ RTT` theoretical
ceiling, i.e. it's the SSH channel window itself, not something else).

**Investigated and consciously not fixed for now:**
- A CGO libssh2/libssh Go binding — the only one that exists
  (`karfield/ssh2go`) is unmaintained; too risky to wire security-critical
  connection code through.
- Shelling out to the system's real `ssh` binary — a real fix (no
  hardcoded window), no CGO, but a controlled benchmark against the same
  isolated test harness (a network namespace with `tc netem`-emulated
  latency, not real internet — see below) found real OpenSSH performing
  about the same as `golang.org/x/crypto/ssh`'s default for a plain
  `exec`-and-discard workload — not the clear win expected from the
  Termius comparison, which undercut the case for taking on this rewrite.
- A Rust helper binary using [`russh`](https://github.com/Eugeny/russh)
  (actively maintained, and — unlike `x/crypto/ssh` — exposes
  `window_size` as a real, public, tunable `Config` field). Benchmarked
  head-to-head against Go's client and real OpenSSH in an isolated network
  namespace with emulated latency (150ms and 300ms RTT, no packet loss,
  300 MiB payload via `dd`, three runs per condition):

  | Client | ~150ms RTT | ~300ms RTT |
  |---|---|---|
  | Go `x/crypto/ssh` (2 MiB, current) | 104.37 Mbit/s | 52.62 Mbit/s |
  | Real OpenSSH | 96.93 Mbit/s | 48.90 Mbit/s |
  | Russh @ 2 MiB (control) | 56.20 Mbit/s | 28.17 Mbit/s |
  | Russh @ 32 MiB | 131.95 Mbit/s | 68.64 Mbit/s |

  This confirms the window-size mechanism cleanly (same Russh binary,
  only the window changed: 2.35-2.44x) and shows Russh-with-a-large-window
  as the one clear, reproducible win of the three alternatives — but the
  measured improvement (~1.3x over Go at both RTT tiers) doesn't fully
  explain the original ~5x Termius gap (real links have packet loss,
  which compounds far worse with a small window than pure latency does,
  and Termius has its own SSH implementation, not vanilla OpenSSH — this
  synthetic test doesn't capture either factor).

**Decision: keep `golang.org/x/crypto/ssh` for now.** The fix path
(a Rust/`russh` helper subprocess, communicating with the Go backend over
pipes with a small framed protocol — full design was worked out and
benchmarked) is real and would help, but it means adding Rust as a second
build toolchain, a new Dockerfile stage, new CI cross-compilation for all
4 release platforms, and reimplementing the TOFU host-key/auth flows
around a new IPC boundary — a substantial, multi-milestone undertaking
for a gap that isn't actually surfacing as a problem in normal use. Worth
revisiting if a concrete workflow (large file transfers, bulk data piped
through a session) makes this bite in practice — the benchmark harness
and design are preserved in case that happens.

---

## 12. Milestones — Shipped (v0.1–v0.2)

Each milestone must compile, pass `go vet` + tests, and meet its acceptance
criteria before starting the next. M0–M7 below are shipped history — kept
for record, not to be redone. New work continues at §12b.

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

### M7 — Session foreground + dashboard overview
Foreground-process lookup and sampler (§4.7), the event fan-out and `/ws/events`
(§5.1), the two new session fields (§6), and the rebuilt dashboard card (§7).
The list poll becomes a fallback for when the event socket is down.
✅ *Verify:* in one session run `sleep 20` → the card names `sleep`; back at the
prompt → it names the shell. `cd` into a subdirectory → the card follows. Run
`sh script.sh` where the script runs `ping` → the card reads `sh › ping`. Create
a session in a second browser tab → it appears in the first at once, not after
5 s. Stop the backend → every dot goes slate; start it → the socket reconnects
and the list comes back.

The derived activity state this milestone originally shipped (`busy` / `waiting`
/ `idle`) was removed again before the release; see the end of §4.7 for why and
for where it is kept.

### M7b — Window title
The OSC 0/2 scanner (§4.8), the `title` field in §6, and the card's second line
(§7). No new endpoint and no schema change: it travels on the session JSON that
already exists. Shipped directly after M7, independently of the v0.4 pivot
below — hence "M7b" rather than continuing the M8+ numbering, which §12b's
milestones (also independently numbered from M8) already claim.
✅ *Verify:* `printf '\033]0;hello\007'` in a session → the card's second line
reads `hello` within a second, and the foreground line above it is unchanged.
Open `vim` → the title follows it; `:q` → the shell's own title comes back. Let
the session exit → both lines clear.

---

## 12b. Milestones — Multi-User Terminal Host Service (v0.4)

Continues directly from M7. One milestone = one commit. `M8` lands first —
every later commit would otherwise contradict `CLAUDE.md`'s hard rules the
moment it landed.

### M8 — Re-scope the docs
`CLAUDE.md` and this document rewritten for the multi-user/SSH-host
direction (no code). ✅ *Verify:* both files describe the system being
built, not the v0.1 one; no lingering "no SSH/no auth" language.

### M9 — Config consolidation
`internal/serverconfig` (`config.yml`); `--root` renamed to
`--workspace-dir` (now optional, defaulting under `--data-dir`);
`--data-dir` defaults to `./data`; `Dockerfile`/`Makefile` updated to the
`/config` + `/workspace` volumes. ✅ *Verify:* `go vet ./... && go test
./...`; a fresh checkout with no flags starts and creates
`./data/config.yml` with hand-editable defaults; `--data-dir`/
`--workspace-dir` set to separate paths keep credentials and the
local-host sandbox on disjoint directories.

### M10 — Auth backend
`internal/auth` (users.yml store, bcrypt, sliding-TTL session store);
bootstrap/login/logout/me endpoints; `requireAuth` gates all existing
`/api/*` and `/ws/*` routes. ✅ *Verify:* fresh `--data-dir` → `/api/auth/status`
reports `needsSetup` → bootstrap creates the admin and sets a cookie → every
previously-open endpoint now 401s without it.

### M11 — Admin user management backend
List/delete/promote/demote endpoints, with a guard that refuses to remove or
demote the last admin. ✅ *Verify:* attempting to demote or delete the sole
admin returns 409; a second admin can be created and then the first removed
cleanly.

### M12 — Frontend auth
`stores/auth.ts`, `LoginPage.vue` (bootstrap/login/register modes), router
guard, admin config panel in `SettingsPage.vue`. ✅ *Verify:* `npm run
build`; visiting any route unauthenticated redirects to `/login`; bootstrap
→ dashboard works end to end in a browser.

### M13 — Frontend admin users page
`UsersPage.vue`, nav entry visible to admins only. ✅ *Verify:* a non-admin
visiting `/admin/users` is redirected away; an admin can promote/demote and
delete, and sees the last-admin 409 surfaced inline.

### M14 — Hosts backend
`internal/hosts` (per-user `hosts.yml`, plaintext credentials, atomic
writes); `/api/hosts*` CRUD, strictly scoped to the caller's own user id,
with masked secret fields in responses. ✅ *Verify:* `go test ./...`; two
different logged-in users each see only their own hosts.

### M15 — Frontend hosts
`stores/hosts.ts`, `HostsPage.vue` + `HostDialog.vue` (grouped list,
add/edit, read-only host-key display), nav entry. ✅ *Verify:* `npm run
build`; create/edit/delete a host end to end in a browser.

### M16 — `session.Backend` interface (pure refactor)
`internal/session/backend.go`; `*terminal.PTY` gains `Read` and now
satisfies it; `Session.pty` renamed to `Session.backend`. Zero behavior
change. ✅ *Verify:* `go vet ./... && go test ./...` pass with **no test
file changes** — the diff is a mechanical rename.

### M17 — SSH-backed sessions, with TOFU host-key trust
`internal/sshpty` (§4.5.1's `Start`, `ProbeHostKey`); host-key probe/trust
endpoints; `Manager.CreateSSH`/`Restart` branching via a `HostResolver`;
sqlite migration adding `user_id`/`target_type`/`host_id`/
`host_display_name`; every session/WS operation becomes owner-scoped.
✅ *Verify:* `go test ./...`; connecting to a host for the first time
returns `host_key_unverified` with a real fingerprint and creates no
session; trusting it then lets the same request through; editing
`hosts.yml`'s pinned fingerprint by hand to a wrong value and reconnecting
returns `host_key_changed`, not a silent connect.

### M18 — Frontend session-creation flow + host-key trust dialog
`NewSessionDialog.vue` rewrite (host picker primary, local secondary gated
on `allowLocalHost`); `HostKeyTrustDialog.vue`. ✅ *Verify:* `npm run
build`; the full "create SSH session → trust prompt → connect → run a
command → refresh → state restored" flow works in a browser, and the
local-host fallback still works exactly as it did before M8.

### M19 — SSH key exchange backend
`sshpty.ExchangeKeys`; `POST /api/hosts/:id/exchange-keys`, never persisting
the supplied password. ✅ *Verify:* `go test ./...`; after a successful
exchange, `hosts.yml` shows `authMethod: privateKey` with the generated key
and no password field, and grepping the data directory for the password
string used in the request finds nothing.

### M20 — SSH key exchange frontend
"Exchange SSH keys" action in `HostDialog.vue`, one-time credentials modal.
✅ *Verify:* `npm run build`; running the action against a real host results
in a subsequent session connecting with no password prompt.

### M21 — Docker Compose + README
`docker-compose.yml` (mounting `./data/config:/config` and
`./data/workspace:/workspace`); README rewritten for the two-volume-under-
one-`./data`-folder story and the web-UI bootstrap flow. ✅ *Verify:* `make
docker` succeeds; `docker compose up` against a fresh `./data` reaches the
bootstrap screen at `:8080`.
### Future (post-v0.4, do not start now)
Search/filter, favorites, tmux import, session sharing, read-only mode.
Everything else this note used to list — rename, SSH remotes, multi-user,
auth — shipped in M8-M21 above.

---

## 12c. Milestones — Host Operations (v0.5)

Process tree, file browser, and file transfer (§4.10), for local and SSH
sessions, Linux and Windows targets. Ordered so every milestone ships a
working, verifiable slice rather than half of `hostops` and half of the UI.

### M22 — `internal/hostops` core
`Transport` (`localTransport`, `sshTransport` off the session's existing
`*ssh.Client`), `FileTransport` (stdlib for local, `github.com/pkg/sftp` for
SSH), no HTTP surface yet. ✅ *Verify:* `go test ./...`; against a real
throwaway SSH user (this repo's established pattern), list/read/write/
rename/remove round-trip correctly, and a directory `Remove` with nested
files actually empties before removing the top directory.

### M23 — Process tree
`Platform` (`linuxPlatform` first; `windowsPlatform` once a Windows SSH
target is available to test against), `GET .../hostops/process-tree`,
`ProcessTreePanel.vue`, rooted at the session's own foreground PID.
✅ *Verify:* open a session, run `sleep 100 & sleep 200 &`, the panel shows
both as children of the shell; kill one, the panel drops it within a
sampler tick.

### M24 — File browser (read-only) + move
`GET .../hostops/files`, `POST .../hostops/move`, `FileBrowserPanel.vue`
(list + rename/move; no copy/delete/transfer yet). ✅ *Verify:* browse a
real directory tree over both a local and an SSH session; move a file into
a subdirectory and back.

### M25 — Copy + Delete, with progress
`POST .../hostops/copy`, `DELETE .../hostops/files`, `GET
.../hostops/ops/:opId`, the `hostop*` messages on `/ws/events` (§5.2), a
progress bar in `FileBrowserPanel.vue`. ✅ *Verify:* delete a directory with
enough files that the progress bar visibly moves before completion; copy a
large file and confirm the byte progress reaches the source file's size
exactly once, not more.

### M26 — Download + Upload
`GET .../hostops/download`, `POST .../hostops/upload` (own body-size
ceiling, §11), browser-native transfer progress in `FileBrowserPanel.vue`.
✅ *Verify:* download a file, checksum matches the source; upload a file
larger than the JSON-endpoint body cap and confirm it is not rejected by
that cap.

---

## 13. Testing Strategy

- **Unit (Go):** RingBuffer (wraparound, exact-boundary), path sandbox
  (§4.5 cases), shell allowlist, session state transitions, the replay filter
  and the alternate-screen check (§8 — sequences split across chunk boundaries
  and BEL as an OSC terminator are the two cases a naive scan gets wrong), the
  foreground chain's label (§4.7), the title scanner (§4.8 — a sequence split
  across chunks, and the OSC operations that look like a title and are not), and
  the event fan-out including its slow-subscriber drop.
- **Integration (Go):** `httptest` + real PTY: create → attach → I/O →
  replay → delete. Use `sh -c 'echo READY; cat'` as a deterministic shell
  for tests instead of bash. `internal/hostops` (§4.10, §12c) additionally
  gets a real-SSH integration pass — a throwaway system user, same as the
  SSH-backend tests already use — since `FileTransport`'s SSH
  implementation is a real `pkg/sftp` client and a fake wire response would
  not catch what an actual `sftp-server` disagrees with the library about.
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
   is looking at. The places that do walk the byte stream — `sanitizeReplay`
   (§8), `endsInAltScreen`, `titleScanner` (§4.8) and `modeScanner` (§4.9) — do
   not weaken this: they step over escape sequences to drop a query, to read a
   title out of one, or to keep a dozen mode flags, and never look at a
   printable character. None of them builds a grid, and none can answer "what is
   on the screen".

   This is the line that keeps the dashboard from showing a preview of each
   session's screen. That feature needs `tmux capture-pane`, which needs
   tmux's `grid.c` — and a second, less complete emulator would disagree with
   xterm.js exactly in the cases where the answer matters, with no way for the
   user to tell which one is lying.
3. One writer goroutine per WS connection; broadcasts never block. This
   still holds for SSH-backed sessions — they reuse `Manager`'s existing
   `readLoop`/`ws.Client` machinery through the `Backend` interface (§4.2),
   not a parallel implementation.
4. Every user-supplied path goes through the sandbox function. No exceptions.
5. Every session/host lookup is scoped to the authenticated user; a
   client-supplied user id is never trusted. This is §4's path-sandbox
   principle applied to identity: `Manager`'s `userID`-scoped methods (§4.3)
   and `internal/hosts.Store` being opened strictly by the session's own
   user id (§6) are its two instances. An unauthorized id probe must be
   indistinguishable from a nonexistent one.
6. SSH host-key trust is explicit and user-driven, never silent (§4.5.1).
   `ssh.InsecureIgnoreHostKey()` is never acceptable, including as a
   shortcut during development.
7. Decisions in this spec are final for the milestones they cover; do not
   introduce alternative libraries or extra features without updating this
   document.
