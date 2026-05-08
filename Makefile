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
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/calibrate-fit ./cmd/calibrate-fit
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/edge-scan ./cmd/edge-scan
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/paper-trader ./cmd/paper-trader
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/settle ./cmd/settle
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/score ./cmd/score
	$(GO) build $(GOFLAGS) -o $(BIN_DIR)/fillability ./cmd/fillability

# Cross-compile for the production Vultr box (linux/amd64). Output goes
# under bin/linux-amd64/. Pair with deploy/install.sh on the target host.
.PHONY: build-linux
build-linux:
	@mkdir -p $(BIN_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux-amd64/tape ./cmd/tape
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux-amd64/paper-trader ./cmd/paper-trader
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/linux-amd64/settle ./cmd/settle

# Bundle linux binaries + deploy units + config example into a tarball
# ready for scp. The .env is NOT included (deploy script expects it
# staged separately on the host). Climate obs CSVs are bundled IF
# present locally — they're optional for the trader (falls back to
# naive per-bracket) but required for the climate_shrunk model.
.PHONY: release
release: build-linux
	@mkdir -p dist
	@INCLUDES="bin/linux-amd64 deploy config/config.example.yaml"; \
	if [[ -d data/calibration/obs ]]; then \
	  INCLUDES="$$INCLUDES data/calibration/obs"; \
	fi; \
	tar -czf dist/isobar-deploy.tar.gz $$INCLUDES
	@echo
	@echo "release artifact: dist/isobar-deploy.tar.gz"
	@echo "deploy:"
	@echo "  scp dist/isobar-deploy.tar.gz HOST:/tmp/"
	@echo "  scp .env HOST:/tmp/isobar.env"
	@echo "  ssh HOST 'cd /tmp && tar -xzf isobar-deploy.tar.gz && cp /tmp/isobar.env .env && sudo bash deploy/install.sh /tmp'"

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
