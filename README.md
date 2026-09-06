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

Working on sessile? See [`docs/dev/`](docs/dev/) — the design
specification, the development setup and the project layout live there.

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
- **Optional local-host sessions.** An admin can allow sessions against the
  server's own shell. Off by default. Everyone permitted to use them shares
  one workspace root — it is not per-user.
- **Files and processes for every session.** A panel beside the terminal
  shows the session's process tree and browses the files on its target —
  local or remote alike. Move, copy and delete with progress, download a
  file to the browser, upload one back, or copy a path to paste into the
  shell next to it.
- **Persistent sessions.** Once connected, PTYs are owned by the backend and
  survive browser disconnects, refreshes and closed tabs.
- **Scrollback restoration.** Each session keeps a ring buffer of its raw
  output bytes; on (re)connect the buffer is replayed and xterm.js
  re-renders the ANSI stream — no server-side terminal emulation.
- **Restart after a backend restart.** A stopped session can be brought back
  under the same id with one click: same target, its scrollback replayed,
  and (for local sessions) arrow-up still walking the commands typed in that
  session.
- **Multi-client.** Several browsers can attach to the same session and see it
  mirrored live, with per-session client counts. The session is sized to the
  smallest window attached to it, so its output fits every one of them — a
  larger window has unused space, as a tmux client does, and gets it back when
  the smaller one disconnects.
- **Sessions that say what they are doing.** Every card names the program in
  the session's foreground — read from the pty and the kernel, not guessed — and
  under it the window title that program set for itself, the same `ESC ] 0 ;`
  sequence a desktop terminal puts in its title bar. Both are kept current about
  once a second for every session, attached or not.
- **Resilient UI.** Automatic reconnect with exponential backoff, a session tab
  bar, and a responsive layout that adapts from desktop to mobile.
- **Usable by touch.** A one-finger drag scrolls the backlog and keeps coasting
  after a flick. Where a program draws its own screen — `less`, `htop`, an
  editor — the drag reaches the program instead, exactly as a mouse wheel does
  on a desktop, since the alternate screen has no scrollback to move. An
  on-screen bar supplies what a phone keyboard has no keys for: Ctrl, Alt and
  Shift as sticky modifiers, plus Esc, Tab, the arrows, Home/End, PgUp/PgDn and
  Del.
- **GPU rendering.** The terminal draws through WebGL, the renderer VS Code's
  terminal uses. Where no WebGL context can be had — an old device, a
  blocklisted driver, or a context a mobile browser reclaims from a backgrounded
  tab — it falls back to xterm's DOM renderer, which is slower but no less
  correct.
- **Installable as an app.** Ships a web manifest, so Chrome/Edge (desktop
  and Android) can pin it as a standalone window with no address bar — from
  the page, either the browser's install icon or "Cast, save, and
  share → Create shortcut…" with "Open as window" checked.
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
- **Local and SSH sessions behave identically.** Attaching, scrollback,
  restart, multi-client, the files-and-processes panel — all of it works the
  same way whichever kind of shell is at the other end.
- Output is read once per session and fanned out to everyone attached. **A
  client that cannot keep up is disconnected** rather than allowed to slow
  the others down; it reconnects and replays from the buffer.
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
      - ./data/workspace:/workspace # where local-host sessions start, if allowLocalHost is on
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
| `--workspace-dir` | `TSM_WORKSPACE_DIR` | `<data-dir>/workspace` (Docker: `/workspace`) — where local-host sessions start and the directory API file operations are confined to; only used when `allowLocalHost` is on |
| `--shells` | `TSM_SHELLS` | `bash,zsh,fish` — local-host shell allowlist only; irrelevant unless `allowLocalHost` is on |
| `--buffer-size` | `TSM_BUFFER_SIZE` | `524288` (bytes) |
| `--session-retention` | `TSM_SESSION_RETENTION` | `0` (keep forever); a Go duration, e.g. `720h`, not `30d` |
| `--log-level` | `TSM_LOG_LEVEL` | `info` |
| `--allow-origin` | `TSM_ALLOW_ORIGIN` | *(none)* — one additional origin accepted for WebSocket upgrades |
| `--insecure-cookies` | `TSM_INSECURE_COOKIES` | `false` — drops the session cookie's `Secure` attribute. Needed to log in at all when serving over plain HTTP on anything but `localhost`: browsers silently discard a `Secure` cookie from an `http://` origin, so login returns 200 and the app bounces straight back to the login form with no error. The real fix is HTTPS. |

`--version` prints the version and exits; `--help` lists every flag.

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

## Security & operational notes

- **Auth:** username + bcrypt-hashed password, server-side session tokens in
  an **in-memory** store with a **sliding 30-day TTL** (renewed on every
  authenticated request), delivered via an `HttpOnly`, `SameSite=Lax` cookie
  (`Secure` unless `--insecure-cookies`). A server restart logs everyone out — accepted,
  since nothing about it is persisted by design.
- **Every session and host lookup is scoped to the authenticated user** — a
  client-supplied id you don't own is indistinguishable from one that
  doesn't exist, including for admins: session visibility is strictly
  per-owner, with no cross-user oversight.
- **Host credentials are plaintext in each user's `hosts.yml`, by design.**
  The operator is the trusted owner of their own server and edits this file
  by hand. Anyone who can read that file has those credentials, so keep the
  data directory to the account running sessile.
- **SSH host-key verification is trust-on-first-use with explicit user
  prompts** — never `ssh.InsecureIgnoreHostKey()`. An unknown or changed key
  blocks the connection (and creates no session) until the user explicitly
  trusts it.
- **"Exchange SSH keys" never persists the password it's given** — used for
  exactly one connection, then discarded; the endpoint clears any previously
  stored password on that host once the exchange succeeds, regardless of
  what the client sends.
- **Paths supplied over the API are checked against `--workspace-dir`** for
  local sessions — `..`, absolute paths and symlinks pointing outside are
  rejected — and the shell must come from the configured allowlist. This
  bounds the API, not the session: the shell it starts is an ordinary
  process and can leave that directory. An SSH session's paths and shell are
  the user's own choice on their own host, and are not restricted here.
- **CSRF:** `SameSite=Lax` plus same-origin JSON `fetch`/WS (no CORS origin
  is ever allowed) keeps CSRF risk low without a token.
- Live sessions do not survive a backend restart (see [How it
  works](#how-it-works)). Scrollback (and, for local sessions, command
  history) does, and the session can be restarted from the UI — but the
  process or connection that was running is gone.
- Stopped sessions are kept forever unless you set `--session-retention`.

## License

MIT — see [`LICENSE`](LICENSE).
