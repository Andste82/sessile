# sessile — build & dev orchestration (PROJECT_PLAN.md §10)
.DEFAULT_GOAL := help

ROOT      ?= $(CURDIR)/sandbox
DATA_DIR  ?= $(ROOT)/data

# The version compiled into the binary and shown on the Settings page. A build
# from a tagged commit reports the tag; every other build reports the commit it
# came from, so a screenshot of that page identifies the code exactly — "dev"
# identified nothing. A release passes VERSION in from the tag and ?= keeps it.
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION   ?= $(if $(GIT_VERSION),$(patsubst v%,%,$(GIT_VERSION)),dev)

# What a locally built image is tagged as — deliberately not the version. The
# point of the dev image is that its name stays put while its contents move, so
# `docker run sessile:dev-ubuntu` keeps meaning "the last one I built".
IMAGE_TAG ?= dev

LDFLAGS   := -s -w -X github.com/Andste82/sessile/backend/internal/config.Version=$(VERSION)

# Where the SPA is embedded from. backend/web/embed.go has `//go:embed all:dist`,
# so this directory must always contain an index.html or the backend does not
# compile — hence the committed placeholder, and hence `clean` restoring it
# rather than deleting the directory outright.
EMBED_DIR := backend/web/dist

.PHONY: help dev-backend dev-frontend test test-backend test-frontend build \
        build-frontend build-backend docker docker-ubuntu clean placeholder tidy

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

dev-backend: ## Run the Go backend against ./sandbox/data in dev mode
	@mkdir -p $(DATA_DIR)
	cd backend && go run ./cmd/server --data-dir=$(DATA_DIR) --dev

dev-frontend: ## Run the Vite dev server (proxies to :8080)
	cd frontend && npm run dev

test: test-backend test-frontend ## Run all tests

test-backend: ## go vet + go test
	cd backend && go vet ./... && go test ./...

test-frontend: ## vitest
	cd frontend && npm run test

build-frontend: ## Build the SPA and copy it into the backend embed dir
	cd frontend && npm run build
	rm -rf $(EMBED_DIR)
	cp -r frontend/dist $(EMBED_DIR)
	@echo "note: $(EMBED_DIR)/index.html now holds the built SPA, not the"
	@echo "      committed placeholder — run 'make clean' before committing."

build-backend: ## Build the single Go binary (embeds the SPA)
	cd backend && CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o ../bin/sessile ./cmd/server

build: build-frontend build-backend ## Full production build → ./bin/sessile
	@echo "built ./bin/sessile"

docker: ## Build the container image (alpine, the default variant)
	docker build --build-arg VERSION=$(VERSION) -t sessile:$(IMAGE_TAG) .

docker-ubuntu: ## Build the ubuntu-based image (glibc, for glibc-linked programs)
	docker build --target runtime-ubuntu --build-arg VERSION=$(VERSION) \
	  -t sessile:$(IMAGE_TAG)-ubuntu .

tidy: ## go mod tidy
	cd backend && go mod tidy

clean: placeholder ## Remove build artifacts, leaving the tree buildable
	rm -rf bin frontend/dist

placeholder: ## Reset the embed dir to the committed placeholder
	rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@printf '%s\n' \
	  '<!doctype html>' \
	  '<html>' \
	  '  <head>' \
	  '    <meta charset="utf-8" />' \
	  '    <title>sessile</title>' \
	  '  </head>' \
	  '  <body>' \
	  '    <p>Frontend not built. Run <code>make build</code> to embed the SPA.</p>' \
	  '  </body>' \
	  '</html>' > $(EMBED_DIR)/index.html
