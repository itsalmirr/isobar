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
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/edge-scan ./cmd/edge-scan
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/calibrate-parse-cli ./cmd/calibrate-parse-cli
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/calibrate-fit ./cmd/calibrate-fit
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/calibrate-bayesian ./cmd/calibrate-bayesian

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
# edge-scan needs --start / --end; printing help is more useful than
# the default "missing required flag" exit-2 a bare `go run` produces.
# Pass real arguments when invoking the binary directly.
run-edge-scan:
	$(GO) run ./cmd/edge-scan --help || true

# backtest: end-to-end realized-PnL backtest wrapper around
# scripts/run-backtest.sh. Required env vars:
#   TAPE   path to the recorded tape (must contain kalshi/ + openmeteo/)
#   START  first civil date to score (YYYY-MM-DD, inclusive)
#   END    last civil date to score  (YYYY-MM-DD, inclusive)
# Optional:
#   EDGE_THRESHOLD  min post-fee edge in cents to enter (default: 5)
#   SKIP_FETCH      set to 1 to reuse already-downloaded CLI bulletins
#
# Example:
#   make backtest TAPE=./data START=2026-04-29 END=2026-05-06
.PHONY: backtest
backtest:
	@test -n "$(TAPE)"  || { echo "ERROR: TAPE=<dir> required" >&2; exit 2; }
	@test -n "$(START)" || { echo "ERROR: START=YYYY-MM-DD required" >&2; exit 2; }
	@test -n "$(END)"   || { echo "ERROR: END=YYYY-MM-DD required" >&2; exit 2; }
	./scripts/run-backtest.sh \
	    --tape  "$(TAPE)" \
	    --start "$(START)" \
	    --end   "$(END)" \
	    $(if $(EDGE_THRESHOLD),--edge-threshold $(EDGE_THRESHOLD),) \
	    $(if $(SKIP_FETCH),--skip-fetch,)

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

.PHONY: install-hooks
install-hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hooks installed (.githooks/)"
