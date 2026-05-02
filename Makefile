SHELL       := /usr/bin/env bash
GO          := go
GOFLAGS     := -trimpath
LDFLAGS     := -s -w \
               -X 'main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)' \
               -X 'main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)' \
               -X 'main.buildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)'

BIN_DIR     := bin
PKGS        := ./...

.PHONY: all
all: lint vet test build

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/tape ./cmd/tape

.PHONY: build-all
build-all: build
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/peek ./cmd/peek
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/analyze ./cmd/analyze
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/calibrate-parse-cli ./cmd/calibrate-parse-cli
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/edge-scan ./cmd/edge-scan

.PHONY: run-tape
run-tape: build
	./$(BIN_DIR)/tape -config config/config.yaml

.PHONY: run-peek
run-peek:
	$(GO) run ./cmd/peek

.PHONY: run-analyze
run-analyze:
	$(GO) run ./cmd/analyze

.PHONY: run-edge-scan
run-edge-scan:
	$(GO) run ./cmd/edge-scan

.PHONY: test
test:
	$(GO) test -race -count=1 -timeout=60s $(PKGS)

.PHONY: test-short
test-short:
	$(GO) test -race -short -count=1 -timeout=30s $(PKGS)

.PHONY: bench
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -benchtime=2s $(PKGS)

.PHONY: coverage
coverage:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

.PHONY: vet
vet:
	$(GO) vet $(PKGS)

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null || { echo "install: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

.PHONY: fmt
fmt:
	$(GO) tool gofumpt -w .
	$(GO) tool goimports -w -local github.com/wh0o0pz/isobar .

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html *.pprof
