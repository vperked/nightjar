# Nightjar: Go backend (backend/) + React/TypeScript frontend (frontend/).
# `make` builds one self-contained binary at bin/nightjar with the UI embedded.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/nightjar
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath

.DEFAULT_GOAL := build
.PHONY: build ui frontend backend agent run dev dev-backend dev-frontend demo test test-backend test-frontend format lint release docker install-service clean help

help: ## Show this help
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'

build: frontend agent backend ## Build the UI, the agent and the binary (bin/nightjar), with both inside it
	@echo "Built $(BIN) ($(VERSION))"

frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci --no-audit --no-fund
	@touch $@

frontend: frontend/node_modules ## Build the React app into the Go embed directory
	cd frontend && npm run build

ui: ## Put a new UI build live without restarting (needs FRONTEND_DIR=<repo>/web/current, see deploy-ui.sh)
	sh deploy-ui.sh

backend: ## Build the Go binary (embeds whatever UI and agent builds exist)
	cd backend && CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../$(BIN) ./cmd/nightjar

AGENT_EMBED := backend/internal/agentbin/dist

agent: ## Build the agent for watching other servers (bin/nightjar-agent-linux-amd64 and -arm64), and pack it for the dashboard to hand out
	cd backend && for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../bin/nightjar-agent-linux-$$arch ./cmd/nightjar-agent; \
	done
	for arch in amd64 arm64; do gzip -9 -n -c bin/nightjar-agent-linux-$$arch > $(AGENT_EMBED)/nightjar-agent-linux-$$arch.gz; done

run: build ## Build, then run with ./.env
	./$(BIN)

dev: ## Backend + Vite dev server with hot reload (Ctrl+C stops both)
	@trap 'kill 0' EXIT; $(MAKE) -s dev-backend & $(MAKE) -s dev-frontend & wait

dev-backend:
	cd backend && go run ./cmd/nightjar -env ../.env

dev-frontend: frontend/node_modules
	cd frontend && npm run dev:live

demo: frontend/node_modules ## UI only, on demo data (no Go or backend needed)
	cd frontend && npm run demo

test: test-backend test-frontend ## Run every test

test-backend:
	cd backend && go vet ./... && go test ./...

test-frontend: frontend/node_modules
	cd frontend && npm run typecheck && npm test

format: frontend/node_modules ## Format the Go code (gofmt) and the frontend (Prettier)
	cd backend && gofmt -w .
	cd frontend && npm run format

lint: frontend/node_modules ## Check formatting, go vet and tsc, changing nothing
	@cd backend && test -z "$$(gofmt -l .)" || { echo "gofmt would change:"; gofmt -l .; exit 1; }
	cd backend && go vet ./...
	cd frontend && npm run format:check && npm run typecheck

release: frontend agent ## Cross-compile linux amd64 + arm64 into dist/
	mkdir -p dist
	cd backend && for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../dist/nightjar-linux-$$arch ./cmd/nightjar; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o ../dist/nightjar-agent-linux-$$arch ./cmd/nightjar-agent; \
	done
	@ls -lh dist/

docker: ## Build the Docker image (nightjar:$(VERSION))
	docker build -t nightjar:$(VERSION) --build-arg VERSION=$(VERSION) .

install-service: build ## Install as a systemd service (needs sudo)
	sudo ./$(BIN) -install-systemd

clean: ## Remove build output
	rm -rf bin dist
	find backend/internal/web/dist $(AGENT_EMBED) -mindepth 1 ! -name .gitkeep -delete
