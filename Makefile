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

.PHONY: run-tape
run-tape: build
	./$(BIN_DIR)/tape -config config/config.yaml

.PHONY: test
test:
	$(GO) test -race -count=1 -timeout=60s $(PKGS)

.PHONY: test-short
test-short:
	$(GO) test -race -short -count=1 -timeout=30s $(PKGS)

.PHONY: bench
bench:
	$(GO) test -run='^$$' -bench=. -benchmem -benchtime=2s $(PKGS)

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
