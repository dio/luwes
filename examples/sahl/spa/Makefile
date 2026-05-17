## spa/Makefile — per-example build, Docker, and smoke-test targets.
##
## Run from this directory: make <target>
## Or from the project root: make -C examples/sahl/spa <target>
##
## Prerequisites:
##   - Go with CGO enabled
##   - zig (downloaded on demand to .bin/ by the zig target)
##   - Docker with buildx (for docker / docker-push targets)
##   - Envoy binary at ../../.bin/envoy (for smoke)

OUTPUT    := libspa.so
IMAGE_TAG ?= spa:latest

# Locate the project root (three directories up: spa/ → sahl/ → examples/ → root).
ROOT := $(realpath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)

ZIG_VERSION := 0.16.0
ZIG_BIN     := $(ROOT)/.bin/zig-dist/zig

# Detect host OS/arch for the zig download URL.
_OS      := $(shell uname -s | tr '[:upper:]' '[:lower:]')
_ARCH    := $(shell uname -m | sed 's/arm64/aarch64/')
ZIG_OS   := $(if $(filter darwin,$(_OS)),macos,$(_OS))
ZIG_URL  := https://ziglang.org/download/$(ZIG_VERSION)/zig-$(_ARCH)-$(ZIG_OS)-$(ZIG_VERSION).tar.xz

.PHONY: all build ui build-linux-amd64 build-linux-arm64 build-linux \
        docker docker-push smoke clean zig

## all: cross-compile both Linux .so files then build the Docker image
all: build-linux docker

## ui: install npm deps and build Vite output (ui/dist/).
## Used by CI (make -C examples/sahl/spa ui) and required before any Go
## build/test of this package (//go:embed ui/dist).
ui:
	npm ci --prefix $(dir $(lastword $(MAKEFILE_LIST)))ui --silent
	npm run build --prefix $(dir $(lastword $(MAKEFILE_LIST)))ui

## build: compile the .so for the current host (local dev / macOS)
build:
	cd $(ROOT) && CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
	  -o examples/sahl/spa/$(OUTPUT) ./examples/sahl/spa/cmd
	@echo "built: $(OUTPUT)"

# ── Zig (on demand) ──────────────────────────────────────────────────────────

## zig: download zig $(ZIG_VERSION) if not already present
zig: $(ZIG_BIN)

$(ZIG_BIN):
	@echo "downloading zig $(ZIG_VERSION) for $(_ARCH)-$(ZIG_OS)..."
	@mkdir -p $(ROOT)/.bin/zig-dist
	@curl -fsSL "$(ZIG_URL)" | tar -xJ --strip-components=1 -C $(ROOT)/.bin/zig-dist
	@echo "zig $(ZIG_VERSION) ready at $(ZIG_BIN)"

# ── Cross-compiled Linux builds ───────────────────────────────────────────────

## build-linux-amd64: cross-compile libspa.linux-amd64.so via zig cc
build-linux-amd64: $(ZIG_BIN)
	@$(MAKE) _cross-build GOARCH=amd64 ZIG_TARGET=x86_64-linux-gnu OUT=libspa.linux-amd64.so

## build-linux-arm64: cross-compile libspa.linux-arm64.so via zig cc
build-linux-arm64: $(ZIG_BIN)
	@$(MAKE) _cross-build GOARCH=arm64 ZIG_TARGET=aarch64-linux-gnu OUT=libspa.linux-arm64.so

## build-linux: cross-compile both amd64 and arm64
build-linux: build-linux-amd64 build-linux-arm64

# Internal target — do not call directly.
_cross-build:
	@printf '#!/usr/bin/env bash\nargs=()\nfor arg in "$$@"; do case "$$arg" in --unresolved-symbols=*) ;; -Wl,--unresolved-symbols=*) ;; *) args+=("$$arg") ;; esac; done\nexec $(ZIG_BIN) cc -target $(ZIG_TARGET) "$${args[@]}"\n' \
	  > /tmp/zigcc-$(ZIG_TARGET).sh
	@chmod +x /tmp/zigcc-$(ZIG_TARGET).sh
	cd $(ROOT) && CC=/tmp/zigcc-$(ZIG_TARGET).sh CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) \
	  go build -trimpath -buildmode=c-shared \
	  -o examples/sahl/spa/$(OUT) ./examples/sahl/spa/cmd
	@echo "built: $(OUT)"

# ── Docker ────────────────────────────────────────────────────────────────────

## docker: cross-compile on host, package into distroless multi-arch image (loads locally)
docker: build-linux
	docker buildx build \
	  --platform linux/amd64,linux/arm64 \
	  --load \
	  -t $(IMAGE_TAG) .

## docker-push: same as docker but pushes to a registry
## Usage: make docker-push IMAGE_TAG=ghcr.io/dio/luwes/spa:latest
docker-push: build-linux
	docker buildx build \
	  --platform linux/amd64,linux/arm64 \
	  --push \
	  -t $(IMAGE_TAG) .

## smoke: run the container and verify all endpoints respond correctly
## Builds a local image first if IMAGE_TAG is the default.
smoke: _smoke-build
	@echo "--- starting container..."
	@docker run -d --rm --name spa-smoke -p 10002:10000 $(IMAGE_TAG)
	@trap 'docker stop spa-smoke 2>/dev/null; exit' INT TERM; \
	for i in $$(seq 1 15); do \
	  curl -sf http://localhost:10002/ >/dev/null 2>&1 && break; \
	  sleep 1; \
	done
	@echo "--- GET / (expect 200 HTML)"
	@curl -sf -o /dev/null -w "  %{http_code} %{content_type}\n" http://localhost:10002/
	@echo "--- GET /about (expect 200 HTML, SPA fallback)"
	@curl -sf -o /dev/null -w "  %{http_code} %{content_type}\n" http://localhost:10002/about
	@echo "--- GET /api/hello (expect 200 JSON)"
	@curl -sf http://localhost:10002/api/hello | \
	  python3 -c "import sys,json; d=json.load(sys.stdin); assert d['filter']=='api-backend'; print(' ', d)"
	@echo "--- GET /api/time (expect 200 JSON with timestamp)"
	@curl -sf http://localhost:10002/api/time | \
	  python3 -c "import sys,json; d=json.load(sys.stdin); assert 'T' in d['time']; print(' ', d)"
	@echo "--- GET /api/unknown (expect 404)"
	@curl -sf -o /dev/null -w "  %{http_code}\n" http://localhost:10002/api/unknown || true
	@echo "--- all smoke checks passed"
	@docker stop spa-smoke

_smoke-build:
	@if [ "$(IMAGE_TAG)" = "spa:latest" ]; then \
	  docker buildx build --platform linux/amd64 --load -t $(IMAGE_TAG) . 2>/dev/null || \
	  docker buildx build --platform linux/arm64 --load -t $(IMAGE_TAG) .; \
	fi

## clean: remove compiled .so files
clean:
	rm -f $(OUTPUT) $(OUTPUT:.so=.h) libspa.linux-amd64.so libspa.linux-arm64.so
