# sessile — build & dev orchestration (PROJECT_PLAN.md §10)
.DEFAULT_GOAL := help

ROOT      ?= $(CURDIR)/sandbox
VERSION   ?= 0.1.0-dev
LDFLAGS   := -s -w -X github.com/Andste82/sessile/backend/internal/config.Version=$(VERSION)

.PHONY: help dev-backend dev-frontend test test-backend test-frontend build \
        build-frontend build-backend docker clean tidy

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

dev-backend: ## Run the Go backend against ./sandbox in dev mode
	@mkdir -p $(ROOT)
	cd backend && go run ./cmd/server --root=$(ROOT) --dev

dev-frontend: ## Run the Vite dev server (proxies to :8080)
	cd frontend && npm run dev

test: test-backend test-frontend ## Run all tests

test-backend: ## go vet + go test
	cd backend && go vet ./... && go test ./...

test-frontend: ## vitest
	cd frontend && npm run test

build-frontend: ## Build the SPA and copy it into the backend embed dir
	cd frontend && npm run build
	rm -rf backend/web/dist
	cp -r frontend/dist backend/web/dist

build-backend: ## Build the single Go binary (embeds the SPA)
	cd backend && CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o ../bin/sessile ./cmd/server

build: build-frontend build-backend ## Full production build → ./bin/sessile
	@echo "built ./bin/sessile"

docker: ## Build the container image
	docker build -t sessile:$(VERSION) .

tidy: ## go mod tidy
	cd backend && go mod tidy

clean: ## Remove build artifacts
	rm -rf bin frontend/dist backend/web/dist
