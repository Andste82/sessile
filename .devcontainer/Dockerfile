FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

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
#   (Go tarball)                 backend toolchain — official 1.25.x baked below
#                                (apt's golang-go is 1.22; modernc.org/sqlite
#                                needs >= 1.25). Works offline / GOTOOLCHAIN=local.
#   (nodejs LTS)                 frontend build/test (Vite, vitest) + Claude Code
#   gh                           GitHub CLI (auth, PRs)
# =============================================================================
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl wget git jq make gnupg \
    bash zsh fish \
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

# --- Node.js LTS + Claude Code ------------------------------------------------
RUN curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code

WORKDIR /workspace

CMD ["/bin/bash"]
