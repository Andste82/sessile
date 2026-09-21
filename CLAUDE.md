# CLAUDE.md — Terminal Host Session Service

Read `docs/dev/PROJECT_PLAN.md` first. It is the single source of truth. This file is
operational guidance for working in this repo.

## What this project is
A browser-based, multi-user terminal session manager (tmux-like) for SSH-reachable
hosts. Users log in, configure their own SSH hosts, and open persistent terminal
sessions against them; an admin may additionally allow local-shell sessions on the
server itself. Since v0.8 a session can also be a **task** (plan §4.12): a
task folder on the target, the main repo cloned into it, optionally a
devcontainer, and the user's own coding agent (Claude Code, Codex, Gemini)
started in plan mode, with the user's notes as context and their scripts as
MCP tools. Backend: Go + Gin + gorilla/websocket + creack/pty +
golang.org/x/crypto (ssh, bcrypt) + modernc.org/sqlite + gopkg.in/yaml.v3.
Frontend: Vue 3 + TS + Vite + Tailwind + @xterm/xterm.

## Hard rules
- **Scope:** SSH-backed sessions and multi-user auth are in scope. Also in
  scope, as a **fixed, named operation set only** (`internal/hostops`, plan
  §4.10): a session's process tree; listing, moving, copying, and deleting
  files on its own target; downloading one file to the browser and
  uploading one back. Every one of those is a specific typed method
  (`ProcessTree`, `ListDir`, `Move(src,dst)`, `Copy(src,dst)`,
  `Delete(path)`, `Download(path)`, `Upload(path,data)`) — never a
  caller-supplied command line, shell pattern, or a caller-chosen list of
  targets. Still out of scope: an in-app text editor for those files (the
  read/write plumbing that download/upload already needs is not the same
  decision as building an editor UI on top of it — that stays a later,
  separate call), Docker/K8s management, host monitoring dashboards
  (CPU/RAM/disk/service), server inventory, RDP/VNC, and — the actual
  boundary here — any endpoint that takes an arbitrary command string or
  acts on more than one explicit src/dst/path argument. If a change drifts
  toward that, stop.
  **v0.8 widened the scope on purpose** (plan §1, §4.12–§4.17): tasks,
  agent connections, notes, script extensions, Git accounts, and the
  `sessile` MCP tunnel are in. The boundary above still holds inside them:
  - A task is a typed `TaskSpec`. Agents come from the built-in registry
    with **constant argv**; the request and the env go to files and to the
    process environment. No API field ever becomes a command line.
  - **`run` is the one exception, and it is an agent tool, not an API.**
    An agent may run a command on **the host of the task it belongs to**:
    one host, never a caller-chosen one, never a list; owner-scoped;
    logged with its exit code; reachable only through that task's own MCP
    socket. Sessile's HTTP API still takes no command string — the typed
    `hostops` operations remain the only file and process surface it
    exposes. Widening that (a host id in the call, a fan-out, an HTTP
    route) is the thing to stop at.
  - Devcontainers are *used* through the host's `devcontainer` CLI
    (`up`/`exec`, a fixed set of mounts), never managed.
  - **Agents run on the sessile server** (v0.9), one per task, and reach
    their host through the task's own SSH connection — SFTP for files,
    exec channels for commands. Nothing is installed on a host and no
    agent token is delivered there. A task also opens the user's own shell
    on its host; the two sessions share a task id.
  - **A task's agent is confined** (`internal/confine`, Landlock): its own
    folder and the programs it needs, nothing else — never `--data-dir`.
    Where the kernel cannot enforce it, a task refuses to start unless the
    operator passes `--allow-unconfined-agents`. Don't widen the ruleset
    to make something convenient work; find what the agent actually needs
    and allow exactly that (§4.12.9).
  - Sessile has **no LLM client**. The only vendor calls it makes are a
    connection Test and a model list, over plain HTTP. A Claude
    subscription token is only ever used by Claude Code running
    interactively in a session — never for an API call, never with
    `claude -p` on sessile's behalf.
  - Agent credentials are tokens/API keys the user gave sessile
    (connections). Don't add host-login checks, copy login files, or mount
    them.
- **Security posture, by design, not by accident:**
  - Host credentials (SSH password / private key) are stored **inline,
    plaintext** in each user's `hosts.yml` — the operator is the trusted owner
    of their own server and edits this file by hand. This is tracked as a
    `// TODO(security):` for optional future encryption-at-rest, not a bug to
    silently "fix."
  - SSH host-key verification is **trust-on-first-use with explicit user
    prompts** (`internal/sshpty`) — never downgrade this to
    `ssh.InsecureIgnoreHostKey()` or any other silent-accept behavior. A
    changed host key must always block the connection until the user
    explicitly confirms it.
  - The "Exchange SSH keys" flow (`internal/sshpty.ExchangeKeys`) must never
    persist the password it's given — it is used for exactly one SSH dial and
    discarded.
  - Every session/host lookup must be scoped to the authenticated user; a
    client-supplied user id is never trusted (mirrors the path-validation
    precedent below). The same goes for tasks, notes, scripts, connections
    and Git accounts.
  - **Scripts run on the sessile server, for every user**, as sessile's OS
    user — equal to shell access there, documented as such (plan §11), and
    never to be described as a sandbox. `allowAgentScripts` is the
    operator's off switch.
  - Secrets that belong to sessile (script settings, connection tokens, Git
    tokens) are never returned by the API and never reach an LLM through
    sessile. Script output is redacted before it reaches the agent, the UI
    or the logs. Script code (`scripts/<name>/`) and its settings
    (`settings/<name>.yml`) are stored apart, so an exported zip can't
    leak a token. `agent.yml` and script settings are plaintext like
    `hosts.yml` (same `// TODO(security):`).
  - Write-effect script calls wait for the user's approval in sessile; the
    agent's own permission mode can't bypass that.
- **Stack:** Do not add GORM, sqlc, zap, viper, socket.io, or an E2E test
  framework. `golang.org/x/crypto` (bcrypt, ssh), `gopkg.in/yaml.v3`, and
  `github.com/pkg/sftp` (host file operations over the session's existing
  SSH connection, plan §4.10 — no separate dial, no new trust decision) are
  direct, permitted deps. No CGO (`CGO_ENABLED=0` must build).
  `golang.org/x/crypto/ssh` has a known, unconfigurable per-channel
  throughput ceiling on high-latency links (fixed 2 MiB window) —
  investigated, benchmarked against alternatives (real OpenSSH, a Rust
  `russh`-based helper), and deliberately kept as-is for now; not a bug to
  silently "fix" by swapping the SSH client. See PROJECT_PLAN.md §11.1.
  No LLM or MCP SDK: the MCP server is a small hand-written JSON-RPC handler
  (`internal/mcp`); the agent's stdio end is a mode of the server binary.
  User scripts are plain-HTTP Python (`requests`/`urllib`), no vendor SDKs.
  Confinement uses `golang.org/x/sys/unix` (Landlock syscalls) — no new
  dependency for it.
- **Protocol:** Binary WS frames = terminal bytes; text frames = JSON control
  messages exactly as specified in PROJECT_PLAN.md §5 (task events: §5.3).
  Never change the wire format without updating the plan. MCP is not a WS
  protocol: since v0.9 it is a 0600 Unix socket in the agent's own folder on
  the server, with a per-start token, reached by `sessile mcp-bridge <dir>`.
- **Security:** Every path an API caller supplies **for the local host** must
  pass the workspace validation in `internal/session/workspace.go` (plan §4.5)
  — a session's starting directory, the directory browser, every local file
  operation. **An SSH session's paths are exempt by design**, not by
  oversight: they are the user's own on the user's own host, and what bounds
  them is the per-user ownership check on the session (plan §4.10's trust
  boundary), not a root to stay inside. Do not add one, and do not "fix" the
  local check by applying it remotely.
  What that validation is: an input check on a public surface — it stops an
  API caller from reading or writing outside `--workspace-dir`. It is **not**
  confinement of the session. `terminal/pty` sets `cmd.Dir` and nothing else;
  the shell is an ordinary process and `cd /` leaves the workspace. That is
  known and accepted (the foreground sampler already reports `""` for a path
  outside the root), so neither the naming nor the comments may suggest a
  jail — and the check is security-critical all the same, as the thing that
  bounds the API.
  Shells only from the allowlist (local-host sessions only). Host keys are
  pinned per-host; changes require explicit user confirmation (see above).
  A task agent's folder (`<data-dir>/users/<uid>/tasks/<id>`) is sessile's
  own path, not a caller's, so it does not go through that check — what
  bounds it is Landlock (§4.12.9). An SSH task's `tasksDir` is exempt for
  the same reason every SSH path is.
- **Concurrency:** Exactly one writer goroutine per WebSocket connection.
  Broadcasts must never block on a slow client. This applies equally to
  SSH-backed sessions — they reuse the same `Manager`/`ws.Client` machinery as
  local sessions, not a parallel implementation.
- Follow the milestone order in plan §12/§12b/§12c/§12d/§12e/§12f/§12g.
  Finish + verify a milestone before starting the next.

## Commands
```bash
make dev-backend    # backend on :8080 (--insecure-cookies --allow-origin=:5173)
make dev-frontend   # vite dev server on :5173, proxies to :8080
make test           # go vet + go test ./... + vitest
make build           # frontend build + embedded single Go binary
make docker          # multi-stage image build
```

## Verification habits
- After backend changes: `go vet ./... && go test ./...`, then the curl/WS
  walkthrough in `scripts/wstest.sh`.
- After frontend changes: `npm run build` must succeed (type errors fail it).
- Manual smoke test for terminal changes: create session → run `htop` →
  refresh page → state restored → second tab mirrors the first.
- Manual smoke test for auth/host changes: bootstrap admin on a fresh
  `./data` → add an SSH host → confirm the host-key trust prompt appears on
  first connect, not a silent connection.
- Manual smoke test for task changes: create a task on a host with a repo →
  sessile clones it there with the Git account → the agent starts on the
  server in plan mode, having read its instructions → it reads and edits
  files on the host and runs its tests through `run` → `ask` reaches the
  task panel and the answer comes back → restart the server, Restart the
  task, and the agent's conversation resumes.
- After a confinement change, run `go test ./internal/confine/` and then a
  real task: a ruleset that is too tight shows up as a CLI that will not
  start (no temp dir) or hangs (no DNS), not as a test failure.

## Conventions
- Go: stdlib `log/slog`, wrapped errors (`fmt.Errorf("…: %w", err)`), table-
  driven tests, contexts on all blocking ops.
- API errors: `{"error":{"code":"…","message":"…"}}` — reuse the helper in
  `internal/api/errors.go`.
- TS types in `frontend/src/api/types.ts` must mirror the JSON shapes in plan
  §6 exactly; update both together.
- Timestamps: RFC 3339 UTC everywhere.
- Commits: one milestone slice per commit, imperative subject line.
