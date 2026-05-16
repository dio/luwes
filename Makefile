ZIG_VERSION := 0.16.0
ZIG_BIN     := $(CURDIR)/.bin/zig

_OS   := $(shell uname -s | tr '[:upper:]' '[:lower:]')
_ARCH := $(shell uname -m | sed 's/arm64/aarch64/')
ZIG_OS  := $(if $(filter darwin,$(_OS)),macos,$(_OS))
ZIG_URL := https://ziglang.org/download/$(ZIG_VERSION)/zig-$(_ARCH)-$(ZIG_OS)-$(ZIG_VERSION).tar.xz

EXAMPLE     ?= header-auth
EXAMPLE_CMD := ./examples/$(EXAMPLE)/cmd

.PHONY: all
all: build

# Download zig on demand
$(ZIG_BIN):
	@mkdir -p .bin
	@echo "Downloading zig $(ZIG_VERSION)..."
	@curl -fsSL "$(ZIG_URL)" | tar -xJ --strip-components=1 -C .bin
	@echo "Zig ready: $@"

# Build for host (dev/test)
.PHONY: build
build:
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).so $(EXAMPLE_CMD)

# Cross-compile for Linux amd64
.PHONY: build-linux-amd64
build-linux-amd64: $(ZIG_BIN)
	TARGET=x86_64-linux-gnu.2.28 \
	CC=$(CURDIR)/scripts/zigcc.sh \
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).linux-amd64.so $(EXAMPLE_CMD)

# Cross-compile for Linux arm64
.PHONY: build-linux-arm64
build-linux-arm64: $(ZIG_BIN)
	TARGET=aarch64-linux-gnu.2.28 \
	CC=$(CURDIR)/scripts/zigcc.sh \
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
	go build -trimpath -buildmode=c-shared \
		-o dist/lib$(EXAMPLE).linux-arm64.so $(EXAMPLE_CMD)

.PHONY: build-linux
build-linux: build-linux-amd64 build-linux-arm64

# Verify ELF output
.PHONY: verify
verify:
	@file dist/lib$(EXAMPLE).linux-amd64.so | grep -q 'ELF 64-bit' \
		&& echo "amd64: OK" || echo "amd64: FAIL"
	@file dist/lib$(EXAMPLE).linux-arm64.so | grep -q 'ELF 64-bit' \
		&& echo "arm64: OK" || echo "arm64: FAIL"

# Run tests (no CGO needed -- uses fake SDK)
.PHONY: test
test:
	go test -race ./...

# Run benchmarks and capture allocs
.PHONY: bench
bench:
	go test -bench=. -benchmem -count=5 ./bench/ | tee bench/results.txt

# Run benchmarks and emit heap profile
.PHONY: bench-profile
bench-profile:
	go test -bench=. -benchmem -memprofile=bench/mem.out ./bench/
	go tool pprof -alloc_objects -http=:8080 bench/mem.out

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
