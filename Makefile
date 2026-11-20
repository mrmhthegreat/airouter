# Makefile for airouter - try `make help` for a list of targets.

# Force bash so case/esac and glob expansion behave the same on every system
# (the default /bin/sh on some platforms is dash, which mishandles our
# release-zip glob loop).
SHELL := /bin/bash

GO ?= go
CGO_ENABLED ?= 1
BIN_DIR := bin
CLI_BIN := $(BIN_DIR)/airouter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Cross-compile targets: (GOOS, GOARCH) pairs. We build with CGO_ENABLED=0 so
# the modernc.org/sqlite pure-Go driver is used - no C cross-compiler needed.
# This means the release binaries run on any matching OS/arch without external
# shared libraries.
TARGETS := \
    linux/amd64   \
    linux/arm64   \
    darwin/amd64  \
    darwin/arm64  \
    windows/amd64 \
    windows/arm64

.PHONY: rename help build build-pure build-cgo test test-verbose test-pure vet fmt tidy clean install run-serve release release-zip docker docker-run

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## Build the CLI into ./bin/airouter (CGO by default)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN) ./cmd/airouter
	@echo "built $(CLI_BIN)"

build-pure: ## Build the pure-Go (no CGO) binary - useful on Windows without a C compiler
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN)-pure ./cmd/airouter
	@echo "built $(CLI_BIN)-pure"

build-cgo: ## Build with CGO explicitly (mattn/go-sqlite3 - faster)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -ldflags "$(LDFLAGS)" -o $(CLI_BIN)-cgo ./cmd/airouter
	@echo "built $(CLI_BIN)-cgo"

# release builds a binary for every (GOOS, GOARCH) pair in $(TARGETS) and
# leaves them in dist/. Pure-Go (no CGO) so no C cross-compiler is needed -
# this works on any Linux/Mac dev machine out of the box.
release: ## Cross-compile release binaries for linux/windows/mac (amd64+arm64) into dist/
	@mkdir -p dist
	@for target in $(TARGETS); do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		out=dist/airouter-$(VERSION)-$$os-$$arch$$ext; \
		echo "==> building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $$out ./cmd/airouter || exit 1; \
	done
	@echo ""
	@echo "release binaries in dist/:"
	@ls -lh dist/ | awk 'NR>1 {printf "  %s  %s\n", $$5, $$9}'

release-zip: release ## Cross-compile and zip/tar every release binary
	@cd dist && for f in airouter-*; do \
		case "$$f" in \
			*.exe) zip -q "$$f.zip" "$$f" && rm -f "$$f" ;; \
			*)     tar -czf "$$f.tar.gz" "$$f" && rm -f "$$f" ;; \
		esac; \
	done
	@echo "archives in dist/:"
	@ls -lh dist/ | awk 'NR>1 {printf "  %s  %s\n", $$5, $$9}'

test: ## Run the test suite
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test -count=1 ./...

test-verbose: ## Run tests with -v
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test -count=1 -v ./...

test-pure: ## Run the test suite with the pure-Go sqlite driver (no CGO)
	CGO_ENABLED=0 $(GO) test -count=1 ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format the code
	$(GO) fmt ./...

tidy: ## Run go mod tidy
	$(GO) mod tidy

clean: ## Remove built artifacts
	rm -rf $(BIN_DIR) dist *.db *.db-wal *.db-shm .airouter-key

install: build ## Install the CLI into $GOBIN
	$(GO) install ./cmd/airouter

run-serve: build ## Run the server on :8080 (UI at /, OpenAI API at /v1)
	AIROUTER_ADMIN_TOKEN=$${AIROUTER_ADMIN_TOKEN:-dev-token} ./$(CLI_BIN) serve -addr :8080

docker: ## Build the docker image (uses the Dockerfile next to this Makefile)
	docker build -t airouter:latest --build-arg VERSION=$(VERSION) .

docker-run: docker ## Run the docker image (mounts ./data as the DB volume)
	@mkdir -p data
	docker run --rm -p 8080:8080 -v $$(pwd)/data:/data airouter:latest

rename: ## Change the Go module path: make rename MODULE=github.com/yourname/airouter
	@test -n "$(MODULE)" || (echo "usage: make rename MODULE=github.com/yourname/airouter"; exit 1)
	@old=$$(awk '/^module /{print $$2}' go.mod); \
	grep -rl --include='*.go' --include='go.mod' --include='*.md' --include='*.sh' "$$old" . | xargs sed -i "s#$$old#$(MODULE)#g"; \
	echo "module is now $(MODULE)"

# Default: build
.DEFAULT_GOAL := build
