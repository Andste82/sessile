# syntax=docker/dockerfile:1

# --- Stage 1: build the frontend -------------------------------------------
# Pinned to BUILDPLATFORM: the SPA is a pile of static files and is identical
# whatever the target arch, so there is nothing to gain from building it under
# emulation — and plenty to lose. Emulated, `npm ci` spawns node workers whose
# JIT output QEMU can kill with SIGILL; npm never reaps the corpse and the build
# hangs until the job times out. Build native, copy the result into the target.
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /app/frontend
# Install deps first for better layer caching.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- Stage 2: build the Go binary (embeds the frontend) --------------------
# Same reasoning as stage 1, and Go needs no emulation to cross-compile: the
# build runs native and GOOS/GOARCH come from the target. CGO is already off,
# so this is a plain cross-compile with no toolchain to install.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend
# Placeholder: a bare `docker build .` must not claim to be a released version.
# The release workflow passes the real value from the git tag.
ARG VERSION=dev
# Supplied by BuildKit; declared here so they reach the RUN below.
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Overlay the freshly-built SPA into the embed directory.
COPY --from=frontend /app/frontend/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags="-s -w -X github.com/Andste82/sessile/backend/internal/config.Version=${VERSION}" \
      -o /sessile ./cmd/server

# --- Stage 3: runtime ------------------------------------------------------
# Two variants, identical behaviour, different userland. The Go binary is static
# (CGO_ENABLED=0) and does not care, but the *shells* run user programs:
#
#   alpine  — ~8 MB base, musl libc, BusyBox coreutils. The default.
#   ubuntu  — ~78 MB base, glibc, GNU coreutils. Pick this when sessions run
#             precompiled binaries or toolchains that assume glibc, which is a
#             common way to lose an afternoon on musl.
#
# Build a specific one with --target runtime-ubuntu / --target runtime-alpine.
# alpine is last, so a bare `docker build .` still produces the default image.

# --- Stage 3a: ubuntu runtime ----------------------------------------------
FROM ubuntu:24.04 AS runtime-ubuntu
# wget is not in the base image and the healthcheck needs it.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         bash ca-certificates tini wget \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data
# Without this the shells inherit glibc's C locale, which is ASCII-only: bash
# rejects a typed umlaut with a BEL. C.UTF-8 is built in and needs no locale
# package. sessile defaults to it too, but set it here so the whole container
# agrees.
ENV LANG=C.UTF-8
COPY --from=backend /sessile /usr/local/bin/sessile

EXPOSE 8080
# Everything sessile keeps — config.yml, users.yml, per-user hosts.yml, the
# local-host workspace (when enabled), the session database, scrollback and
# history — lives under this one directory now (PROJECT_PLAN.md §8, §9).
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

# tini reaps zombies (shells are grandchildren of PID 1).
ENTRYPOINT ["/usr/bin/tini", "--", "sessile"]
CMD ["--data-dir=/data", "--shells=bash"]

# --- Stage 3b: alpine runtime (default) ------------------------------------
# alpine, not scratch: sessions spawn real shells, so bash must be present.
FROM alpine:3 AS runtime-alpine
RUN apk add --no-cache bash ca-certificates tini \
    && mkdir -p /data
# musl treats its C locale as UTF-8, so this is belt-and-braces here — but it
# keeps both variants identical rather than relying on that difference.
ENV LANG=C.UTF-8
COPY --from=backend /sessile /usr/local/bin/sessile

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

# tini reaps zombies (shells are grandchildren of PID 1).
ENTRYPOINT ["/sbin/tini", "--", "sessile"]
CMD ["--data-dir=/data", "--shells=bash"]
