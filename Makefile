GO_TOOL := GOWORK=off go tool -modfile=tools/go.mod

ZIG_VERSION   := 0.16.0
ZIG_BIN       := $(CURDIR)/.bin/zig-dist/zig

ENVOY_VERSION := 1.38.0
ENVOY_BIN     := $(CURDIR)/.bin/envoy

EXAMPLE     ?= header-auth
EXAMPLE_CMD := ./examples/$(EXAMPLE)/cmd
ENVOY_YAML  ?= $(CURDIR)/examples/$(EXAMPLE)/envoy.yaml

GOOS  := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

# Detect host architecture for Zig (uses aarch64/x86_64 naming, not amd64/arm64)
_ARCH := $(shell uname -m | sed 's/arm64/aarch64/')

# Zig download URL (uses macos not darwin, and aarch64/x86_64 arch names)
ZIG_OS  := $(if $(filter darwin,$(GOOS)),macos,$(GOOS))
ZIG_URL := https://ziglang.org/download/$(ZIG_VERSION)/zig-$(_ARCH)-$(ZIG_OS)-$(ZIG_VERSION).tar.xz

# Envoy download URL (archive.tetratelabs.io)
# Supports: darwin-arm64, darwin-amd64, linux-amd64, linux-arm64
ENVOY_URL := https://archive.tetratelabs.io/envoy/download/v$(ENVOY_VERSION)/envoy-v$(ENVOY_VERSION)-$(GOOS)-$(GOARCH).tar.xz

.PHONY: all
all: build

# Download zig on demand
$(ZIG_BIN):
	@mkdir -p .bin/zig-dist
	@echo "Downloading zig $(ZIG_VERSION)..."
	@curl -fsSL "$(ZIG_URL)" | tar -xJ --strip-components=1 -C .bin/zig-dist
	@echo "Zig ready: $@"

# Download envoy on demand
$(ENVOY_BIN):
	@mkdir -p .bin
	@echo "Downloading Envoy $(ENVOY_VERSION) for $(GOOS)-$(GOARCH)..."
	@curl -fsSL "$(ENVOY_URL)" | tar -xJ --strip-components=2 -C .bin
	@chmod +x .bin/envoy
	@echo "Envoy ready: $@"

.PHONY: download-envoy
download-envoy: $(ENVOY_BIN)

# Build for host (dev/test)
.PHONY: build
build:
	@mkdir -p dist
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).so $(EXAMPLE_CMD)

# Cross-compile for Linux amd64
.PHONY: build-linux-amd64
build-linux-amd64: $(ZIG_BIN)
	@mkdir -p dist
	TARGET=x86_64-linux-gnu.2.28 \
	CC=$(CURDIR)/scripts/zigcc.sh \
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).linux-amd64.so $(EXAMPLE_CMD)

# Cross-compile for Linux arm64
.PHONY: build-linux-arm64
build-linux-arm64: $(ZIG_BIN)
	@mkdir -p dist
	TARGET=aarch64-linux-gnu.2.28 \
	CC=$(CURDIR)/scripts/zigcc.sh \
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
	go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).linux-arm64.so $(EXAMPLE_CMD)

.PHONY: build-linux
build-linux: build-linux-amd64 build-linux-arm64

# Cross-compile all examples for Linux amd64 (used by CI)
.PHONY: build-linux-amd64-examples
build-linux-amd64-examples: $(ZIG_BIN)
	@mkdir -p dist
	@for example in hello header-auth observability; do \
		echo "==> building $$example"; \
		TARGET=x86_64-linux-gnu.2.28 \
		CC=$(CURDIR)/scripts/zigcc.sh \
		CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
		go build -trimpath -buildmode=c-shared \
			-o dist/lib$${example}.linux-amd64.so ./examples/$${example}/cmd || exit 1; \
	done

# Verify ELF output
.PHONY: verify
verify:
	@for f in dist/*.linux-amd64.so; do \
		file "$$f" | grep -q 'ELF 64-bit' && echo "OK: $$f" || { echo "FAIL: $$f"; exit 1; }; \
	done

# Start Envoy with the filter (foreground)
.PHONY: run
run: build $(ENVOY_BIN)
	GODEBUG=cgocheck=0 \
	ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(CURDIR)/dist \
	$(ENVOY_BIN) -c $(ENVOY_YAML) --log-level warning

# Capture a pprof allocs flamegraph under load (requires hey)
.PHONY: flamegraph
flamegraph: build $(ENVOY_BIN)
	@mkdir -p bench/profiles
	@echo "Starting Envoy in background..."
	@GODEBUG=cgocheck=0 \
	 ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(CURDIR)/dist \
	 $(ENVOY_BIN) -c $(ENVOY_YAML) --log-level warning &
	@echo "Waiting for Envoy to be ready..."
	@until curl -sf http://127.0.0.1:9901/ready > /dev/null 2>&1; do sleep 0.5; done
	@curl -sf -H "x-api-key: warmup" http://localhost:10000/ > /dev/null || true
	@sleep 1
	@echo "Warming up (50k requests)..."
	@hey -n 50000 -c 100 -H "x-api-key: warmup" http://localhost:10000/ > /dev/null
	@echo "Capturing allocs profile under load..."
	@hey -n 500000 -c 200 -H "x-api-key: bench" http://localhost:10000/ > /dev/null &
	@sleep 2
	@curl -sf http://127.0.0.1:6061/debug/pprof/allocs -o bench/profiles/allocs_$(EXAMPLE).out
	@pkill -f "envoy.*$(notdir $(ENVOY_YAML))" 2>/dev/null || true
	@echo ""
	@echo "Profile saved: bench/profiles/allocs_$(EXAMPLE).out"
	@echo "Top allocations:"
	@go tool pprof -alloc_objects -top bench/profiles/allocs_$(EXAMPLE).out 2>/dev/null | head -15
	@echo ""
	@echo "Generating flamegraph..."
	@PATH="$(CURDIR)/scripts/flamegraph:$$PATH" \
		go-torch -b bench/profiles/allocs_$(EXAMPLE).out --pprofArgs="-alloc_objects" \
		-f bench/profiles/flamegraph_$(EXAMPLE).svg 2>/dev/null && \
		echo "  Flamegraph: bench/profiles/flamegraph_$(EXAMPLE).svg" || \
		echo "  (install go-torch for flamegraph: go install github.com/uber-archive/go-torch@latest)"

# Start otel-front (local OTLP receiver + browser UI)
# Receives on gRPC :4317, HTTP :4318, serves UI on :8000
.PHONY: otel-front
otel-front:
	@echo "Starting otel-front..."
	@echo "  UI:        http://localhost:8000"
	@echo "  OTLP gRPC: localhost:4317"
	@echo "  OTLP HTTP: localhost:4318"
	otel-front

# Start Envoy wired to otel-front (metrics + traces + enriched logs).
# Requires: make otel-front running in another terminal.
# Uses examples/observability/envoy-otel.yaml with stats sink + OTel tracer.
.PHONY: observe
observe: build $(ENVOY_BIN)
	@echo "Open otel-front UI: http://localhost:8000"
	GODEBUG=cgocheck=0 \
	ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(CURDIR)/dist \
	$(ENVOY_BIN) -c examples/observability/envoy-otel.yaml --log-level warning


.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test -race ./...

.PHONY: format
format:
	$(GO_TOOL) golangci-lint fmt

.PHONY: format-check
format-check:
	$(GO_TOOL) golangci-lint fmt --diff .

.PHONY: lint
lint:
	$(GO_TOOL) golangci-lint run --timeout 5m

.PHONY: coverage
coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out

.PHONY: bench
bench:
	go test -bench=. -benchmem -count=5 ./bench/ | tee bench/results.txt
bench-profile:
	go test -bench=. -benchmem -memprofile=bench/mem.out ./bench/
	go tool pprof -alloc_objects -http=:8080 bench/mem.out

# Run e2e tests against a compiled .so and a local Envoy binary.
# Requires: make build-linux-amd64 EXAMPLE=header-auth (on linux)
#           or: make build EXAMPLE=header-auth (on darwin, for local dev)
# ENVOY_BIN defaults to .bin/envoy (downloaded by this Makefile).
# LUWES_SO  defaults to dist/libheader-auth.so.
.PHONY: e2e
e2e: $(ENVOY_BIN)
	ENVOY_BIN=$(ENVOY_BIN) \
	go test -C e2e -v -timeout=90s ./...

# Sync abi.h from envoy at a given commit
# Usage: make sync-abi COMMIT=<hash>
.PHONY: sync-abi
sync-abi:
	./scripts/sync-abi.sh $(COMMIT)

# Check abi.h drift against envoy/main
.PHONY: check-abi-drift
check-abi-drift:
	@LATEST=$$(curl -fsSL https://raw.githubusercontent.com/envoyproxy/envoy/main/source/extensions/dynamic_modules/abi/abi.h) && \
	if ! diff -q <(echo "$$LATEST") abi/abi.h > /dev/null 2>&1; then \
		echo "WARNING: abi.h has drifted from envoy/main"; \
		diff <(echo "$$LATEST") abi/abi.h | head -40; \
	else \
		echo "abi.h is in sync with envoy/main"; \
	fi

.PHONY: clean
clean:
	rm -rf dist/*.so dist/*.h

.PHONY: tidy
tidy:
	go mod tidy

