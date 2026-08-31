# CLAUDE.md — Terminal Host Session Service

Read `docs/PROJECT_PLAN.md` first. It is the single source of truth. This file is
operational guidance for working in this repo.

## What this project is
A browser-based, multi-user terminal session manager (tmux-like) for SSH-reachable
hosts. Users log in, configure their own SSH hosts, and open persistent terminal
sessions against them; an admin may additionally allow local-shell sessions on the
server itself. Backend: Go + Gin + gorilla/websocket + creack/pty +
golang.org/x/crypto (ssh, bcrypt) + modernc.org/sqlite + gopkg.in/yaml.v3.
Frontend: Vue 3 + TS + Vite + Tailwind + @xterm/xterm.

## Hard rules
- **Scope:** SSH-backed sessions and multi-user auth are in scope. Still out of
  scope: SFTP/file manager, upload/download, Docker/K8s management, host
  monitoring dashboards, a script execution framework. If a change drifts
  toward these, stop.
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
    client-supplied user id is never trusted (mirrors the sandbox-check
    precedent below).
- **Stack:** Do not add GORM, sqlc, zap, viper, socket.io, or an E2E test
  framework. `golang.org/x/crypto` (bcrypt, ssh) and `gopkg.in/yaml.v3` are
  direct, permitted deps. No CGO (`CGO_ENABLED=0` must build).
- **Protocol:** Binary WS frames = terminal bytes; text frames = JSON control
  messages exactly as specified in PROJECT_PLAN.md §5. Never change the wire
  format without updating the plan.
- **Security:** Every user-supplied path must pass the sandbox check in
  `internal/session` (plan §4.5). Shells only from the allowlist (local-host
  sessions only). Host keys are pinned per-host; changes require explicit user
  confirmation (see above).
- **Concurrency:** Exactly one writer goroutine per WebSocket connection.
  Broadcasts must never block on a slow client. This applies equally to
  SSH-backed sessions — they reuse the same `Manager`/`ws.Client` machinery as
  local sessions, not a parallel implementation.
- Follow the milestone order in plan §12/§12b. Finish + verify a milestone
  before starting the next.

## Commands
```bash
make dev-backend    # go run ./backend/cmd/server --data-dir=./sandbox/data --dev
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

## Conventions
- Go: stdlib `log/slog`, wrapped errors (`fmt.Errorf("…: %w", err)`), table-
  driven tests, contexts on all blocking ops.
- API errors: `{"error":{"code":"…","message":"…"}}` — reuse the helper in
  `internal/api/errors.go`.
- TS types in `frontend/src/api/types.ts` must mirror the JSON shapes in plan
  §6 exactly; update both together.
- Timestamps: RFC 3339 UTC everywhere.
- Commits: one milestone slice per commit, imperative subject line.
