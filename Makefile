# Server Monitor: Go backend (backend/) + React/TypeScript frontend (frontend/).
# `make` builds one self-contained binary at bin/servermonitor with the UI embedded.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/servermonitor
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath

.DEFAULT_GOAL := build
.PHONY: build frontend backend run dev dev-backend dev-frontend demo test test-backend test-frontend lint release docker install-service clean help

help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'

build: frontend backend ## Build the UI and the binary (bin/servermonitor)
	@echo "Built $(BIN) ($(VERSION))"

frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci --no-audit --no-fund
	@touch $@

frontend: frontend/node_modules ## Build the React app into the Go embed directory
	cd frontend && npm run build

backend: ## Build the Go binary (embeds whatever UI build exists)
	cd backend && CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../$(BIN) ./cmd/servermonitor

run: build ## Build, then run with ./.env
	./$(BIN)

dev: ## Backend + Vite dev server with hot reload (Ctrl+C stops both)
	@trap 'kill 0' EXIT; $(MAKE) -s dev-backend & $(MAKE) -s dev-frontend & wait

dev-backend:
	cd backend && go run ./cmd/servermonitor -env ../.env

dev-frontend: frontend/node_modules
	cd frontend && npm run dev

demo: frontend/node_modules ## UI only, on demo data (no Go or backend needed)
	cd frontend && npm run demo

test: test-backend test-frontend ## Run every test

test-backend:
	cd backend && go vet ./... && go test ./...

test-frontend: frontend/node_modules
	cd frontend && npm run typecheck && npm test

release: frontend ## Cross-compile linux amd64 + arm64 into dist/
	mkdir -p dist
	cd backend && for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../dist/servermonitor-linux-$$arch ./cmd/servermonitor; \
	done
	@ls -lh dist/

docker: ## Build the Docker image (servermonitor:$(VERSION))
	docker build -t servermonitor:$(VERSION) --build-arg VERSION=$(VERSION) .

install-service: build ## Install as a systemd service (needs sudo)
	sudo ./$(BIN) -install-systemd

clean: ## Remove build output
	rm -rf bin dist
	find backend/internal/web/dist -mindepth 1 ! -name .gitkeep -delete
