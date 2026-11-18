# Makefile for airouter — try `make help` for a list of targets.

GO ?= go
CGO_ENABLED ?= 1
BIN_DIR := bin
CLI_BIN := $(BIN_DIR)/airouter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: rename help build build-pure build-cgo test test-verbose vet fmt tidy clean install run-serve

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the CLI into ./bin/airouter (CGO by default)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN) ./cmd/airouter
	@echo "built $(CLI_BIN)"

build-pure: ## Build the pure-Go (no CGO) binary — useful on Windows without a C compiler
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN)-pure ./cmd/airouter
	@echo "built $(CLI_BIN)-pure"

build-cgo: ## Build with CGO explicitly (mattn/go-sqlite3 — faster)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN)-cgo ./cmd/airouter
	@echo "built $(CLI_BIN)-cgo"

test: ## Run the test suite
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test -count=1 ./...

test-verbose: ## Run tests with -v
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test -count=1 -v ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format the code
	$(GO) fmt ./...

tidy: ## Run go mod tidy
	$(GO) mod tidy

clean: ## Remove built artifacts
	rm -rf $(BIN_DIR) *.db *.db-wal *.db-shm

install: build ## Install the CLI into $GOBIN
	$(GO) install ./cmd/airouter

run-serve: build ## Run the server on :8080 (UI at /, OpenAI API at /v1)
	AIROUTER_ADMIN_TOKEN=$${AIROUTER_ADMIN_TOKEN:-dev-token} ./$(CLI_BIN) serve -addr :8080

rename: ## Change the Go module path: make rename MODULE=github.com/yourname/airouter
	@test -n "$(MODULE)" || (echo "usage: make rename MODULE=github.com/yourname/airouter"; exit 1)
	@old=$$(awk '/^module /{print $$2}' go.mod); \
	grep -rl --include='*.go' --include='go.mod' --include='*.md' --include='*.sh' "$$old" . | xargs sed -i "s#$$old#$(MODULE)#g"; \
	echo "module is now $(MODULE)"

# Default: build
.DEFAULT_GOAL := build
