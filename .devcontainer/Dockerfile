FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# ubuntu:24.04 sets no locale, and glibc's default C locale is ASCII-only. That
# reaches sessions started by `make dev-backend`: the backend inherits this env
# and passes it to every shell it spawns, where bash rejects a typed umlaut with
# a BEL and zsh shows it as <ffffffff>. C.UTF-8 is built in and needs no locale
# package.
ENV LANG=C.UTF-8

# =============================================================================
# sessile — dev environment.
#
# sessile is a Go backend (Gin + gorilla/websocket + creack/pty +
# modernc.org/sqlite, CGO-free) that embeds a Vue 3 + TypeScript + Vite SPA.
# The image therefore ships just two toolchains plus a handful of CLI tools:
#
#   git curl wget jq make       repo + Makefile workflow, REST smoke tests
#   ca-certificates gnupg        TLS + apt keyring for the GitHub CLI repo
#   bash zsh fish                the three shells in sessile's default allowlist
#                                (--shells=bash,zsh,fish) so sessions can spawn
#                                any of them during manual testing
#   gcc libc6-dev                only so `go test -race` can run; the shipped
#                                binary stays CGO-free (see below)
#   procps psmisc iproute2       finding and stopping stray test processes by pid
#   setsid (util-linux)          starts a child outside the shell's process group,
#                                which internal/session's terminate tests need
#   (Go tarball)                 backend toolchain — official 1.25.x baked below
#                                (apt's golang-go is 1.22; modernc.org/sqlite
#                                needs >= 1.25). Works offline / GOTOOLCHAIN=local.
#   (nodejs LTS)                 frontend build/test (Vite, vitest) + Claude Code
#   gh                           GitHub CLI (auth, PRs)
#   docker-ce-cli                client only, no daemon — `make docker` talks to
#                                the host's daemon over the socket mounted by
#                                start.sh / devcontainer.json
#   (playwright + chromium)      headless browser, for checking the frontend in
#                                a real engine — see the stanza at the bottom
# =============================================================================
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl wget git jq make gnupg \
    bash zsh fish \
    gcc libc6-dev \
    procps psmisc iproute2 util-linux \
    && rm -rf /var/lib/apt/lists/*

# --- Go (official toolchain) --------------------------------------------------
# apt's golang-go on Ubuntu 24.04 is 1.22; modernc.org/sqlite (and the backend
# go.mod) require >= 1.25. Bake the matching 1.25.x so builds need no
# GOTOOLCHAIN auto-download. arch derived so the image builds on amd64 and arm64.
ENV GOPATH=/root/go
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH
RUN GO_VERSION=1.25.11 && ARCH="$(dpkg --print-architecture)" \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz \
    && tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz \
    && go version

# --- GitHub CLI ---------------------------------------------------------------
RUN mkdir -p -m 755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# --- Docker CLI (docker-outside-of-docker) ------------------------------------
# Client only — no daemon runs in here. start.sh and devcontainer.json bind-mount
# the host's /var/run/docker.sock, so `make docker` builds on the host daemon and
# the resulting image lands in the host's image store. Note this gives the
# container full control of that daemon, which is root-equivalent on the host.
# buildx is included because `docker build` defaults to it on modern clients.
RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
        -o /etc/apt/keyrings/docker.asc \
    && chmod go+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
        > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        docker-ce-cli docker-buildx-plugin \
    && rm -rf /var/lib/apt/lists/*

# --- Node.js LTS + Claude Code ------------------------------------------------
RUN curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code

# --- Headless Chromium (Playwright) -------------------------------------------
# For checking the frontend in a real engine. Two faults were found with it that
# reading the code could not settle, and one of them turned on a difference no
# hand-written harness can produce: events dispatched from page JavaScript carry
# isTrusted: false and take a different path through xterm than a keyboard's do.
# Chromium driven over CDP produces the trusted kind —
# `Input.imeSetComposition` and `Input.insertText` are a real composition — and
# that is how issue #82 was reproduced without a phone. See
# docs/mobile-checklist.md for the recipe.
#
# Installed globally, and deliberately not in frontend/package.json: §2 of the
# plan rules out an E2E framework as a project dependency, and this is a tool
# for the machine rather than something the build or CI needs. Scripts using it
# belong in a scratch directory, not in the repo.
#
# PLAYWRIGHT_BROWSERS_PATH is pinned so the download lands in the image instead
# of in a home directory a volume might later shadow. Only chromium is fetched;
# firefox and webkit would triple the size for nothing.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN npm install -g playwright@1.62.1 \
    && npx --yes playwright@1.62.1 install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*

# --- Race detector ------------------------------------------------------------
# `go test -race` needs cgo, hence gcc above. The shipped binary is unaffected:
# the Makefile and the Dockerfile both build with CGO_ENABLED=0, and that stays
# the rule (§2 — no CGO). Run it explicitly when touching anything concurrent:
#
#   cd backend && CGO_ENABLED=1 go test ./... -race -count=3
ENV CGO_ENABLED=0

WORKDIR /workspace

CMD ["/bin/bash"]
