# syntax=docker/dockerfile:1

# --- Stage 1: build the frontend -------------------------------------------
FROM node:22-alpine AS frontend
WORKDIR /app/frontend
# Install deps first for better layer caching.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- Stage 2: build the Go binary (embeds the frontend) --------------------
FROM golang:1.25-alpine AS backend
ARG VERSION=0.1.0
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Overlay the freshly-built SPA into the embed directory.
COPY --from=frontend /app/frontend/dist ./web/dist
RUN CGO_ENABLED=0 go build \
      -ldflags="-s -w -X github.com/Andste82/sessile/backend/internal/config.Version=${VERSION}" \
      -o /sessile ./cmd/server

# --- Stage 3: runtime ------------------------------------------------------
# alpine (not scratch): sessions spawn real shells, so bash must be present.
FROM alpine:3
RUN apk add --no-cache bash ca-certificates tini \
    && mkdir -p /workspace /config
COPY --from=backend /sessile /usr/local/bin/sessile

EXPOSE 8080
VOLUME ["/config", "/workspace"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

# tini reaps zombies (shells are grandchildren of PID 1).
ENTRYPOINT ["/sbin/tini", "--", "sessile"]
CMD ["--root=/workspace", "--db=/config/sessions.db", "--shells=bash"]
