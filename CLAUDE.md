# CLAUDE.md — Terminal Session Manager

Read `docs/PROJECT_PLAN.md` first. It is the single source of truth. This file is
operational guidance for working in this repo.

## What this project is
A browser-based persistent terminal session manager (tmux-like). Backend: Go +
Gin + gorilla/websocket + creack/pty + modernc.org/sqlite. Frontend: Vue 3 +
TS + Vite + Tailwind + @xterm/xterm.

## Hard rules
- **Scope:** No SSH, SFTP, file manager, Docker/K8s features, monitoring, or
  auth (until v0.4). If a change drifts toward these, stop.
- **Stack:** Do not add GORM, sqlc, zap, viper, socket.io, or an E2E test
  framework. No CGO (`CGO_ENABLED=0` must build).
- **Protocol:** Binary WS frames = terminal bytes; text frames = JSON control
  messages exactly as specified in PROJECT_PLAN.md §5. Never change the wire
  format without updating the plan.
- **Security:** Every user-supplied path must pass the sandbox check in
  `internal/session` (plan §4.5). Shells only from the allowlist.
- **Concurrency:** Exactly one writer goroutine per WebSocket connection.
  Broadcasts must never block on a slow client.
- Follow the milestone order in plan §12. Finish + verify a milestone before
  starting the next.

## Commands
```bash
make dev-backend    # go run ./backend/cmd/server --root=./sandbox --dev
make dev-frontend   # vite dev server on :5173, proxies to :8080
make test           # go vet + go test ./... + vitest
make build          # frontend build + embedded single Go binary
make docker         # multi-stage image build
```

## Verification habits
- After backend changes: `go vet ./... && go test ./...`, then the curl/WS
  walkthrough in `scripts/wstest.sh`.
- After frontend changes: `npm run build` must succeed (type errors fail it).
- Manual smoke test for terminal changes: create session → run `htop` →
  refresh page → state restored → second tab mirrors the first.

## Conventions
- Go: stdlib `log/slog`, wrapped errors (`fmt.Errorf("…: %w", err)`), table-
  driven tests, contexts on all blocking ops.
- API errors: `{"error":{"code":"…","message":"…"}}` — reuse the helper in
  `internal/api/errors.go`.
- TS types in `frontend/src/api/types.ts` must mirror the JSON shapes in plan
  §6 exactly; update both together.
- Timestamps: RFC 3339 UTC everywhere.
- Commits: one milestone slice per commit, imperative subject line.
