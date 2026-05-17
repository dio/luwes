     1|## spa/Makefile — per-example build, Docker, and smoke-test targets.
     2|##
     3|## Run from this directory: make <target>
     4|## Or from the project root: make -C sahl/examples/spa <target>
     5|##
     6|## Prerequisites:
     7|##   - Go with CGO enabled
     8|##   - zig (downloaded on demand to .bin/ by the zig target)
     9|##   - Docker with buildx (for docker / docker-push targets)
    10|##   - Envoy binary at ../../.bin/envoy (for smoke)
    11|
    12|OUTPUT    := libspa.so
    13|IMAGE_TAG ?= spa:latest
    14|
    15|# Locate the project root (three directories up: spa/ → sahl/ → examples/ → root).
    16|ROOT := $(realpath $(dir $(lastword $(MAKEFILE_LIST)))/../../..)
    17|
    18|ZIG_VERSION := 0.16.0
    19|ZIG_BIN     := $(ROOT)/.bin/zig-dist/zig
    20|
    21|# Detect host OS/arch for the zig download URL.
    22|_OS      := $(shell uname -s | tr '[:upper:]' '[:lower:]')
    23|_ARCH    := $(shell uname -m | sed 's/arm64/aarch64/')
    24|ZIG_OS   := $(if $(filter darwin,$(_OS)),macos,$(_OS))
    25|ZIG_URL  := https://ziglang.org/download/$(ZIG_VERSION)/zig-$(_ARCH)-$(ZIG_OS)-$(ZIG_VERSION).tar.xz
    26|
    27|.PHONY: all build ui build-linux-amd64 build-linux-arm64 build-linux \
    28|        docker docker-push smoke clean zig
    29|
    30|## all: cross-compile both Linux .so files then build the Docker image
    31|all: build-linux docker
    32|
    33|## ui: install npm deps and build Vite output (ui/dist/).
    34|## Used by CI (make -C sahl/examples/spa ui) and required before any Go
    35|## build/test of this package (//go:embed ui/dist).
    36|ui:
    37|	npm ci --prefix $(dir $(lastword $(MAKEFILE_LIST)))ui --silent
    38|	npm run build --prefix $(dir $(lastword $(MAKEFILE_LIST)))ui
    39|
    40|## build: compile the .so for the current host (local dev / macOS)
    41|build:
    42|	cd $(ROOT) && CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
    43|	  -o sahl/examples/spa/$(OUTPUT) ./sahl/examples/spa/cmd
    44|	@echo "built: $(OUTPUT)"
    45|
    46|# ── Zig (on demand) ──────────────────────────────────────────────────────────
    47|
    48|## zig: download zig $(ZIG_VERSION) if not already present
    49|zig: $(ZIG_BIN)
    50|
    51|$(ZIG_BIN):
    52|	@echo "downloading zig $(ZIG_VERSION) for $(_ARCH)-$(ZIG_OS)..."
    53|	@mkdir -p $(ROOT)/.bin/zig-dist
    54|	@curl -fsSL "$(ZIG_URL)" | tar -xJ --strip-components=1 -C $(ROOT)/.bin/zig-dist
    55|	@echo "zig $(ZIG_VERSION) ready at $(ZIG_BIN)"
    56|
    57|# ── Cross-compiled Linux builds ───────────────────────────────────────────────
    58|
    59|## build-linux-amd64: cross-compile libspa.linux-amd64.so via zig cc
    60|build-linux-amd64: $(ZIG_BIN)
    61|	@$(MAKE) _cross-build GOARCH=amd64 ZIG_TARGET=x86_64-linux-gnu OUT=libspa.linux-amd64.so
    62|
    63|## build-linux-arm64: cross-compile libspa.linux-arm64.so via zig cc
    64|build-linux-arm64: $(ZIG_BIN)
    65|	@$(MAKE) _cross-build GOARCH=arm64 ZIG_TARGET=aarch64-linux-gnu OUT=libspa.linux-arm64.so
    66|
    67|## build-linux: cross-compile both amd64 and arm64
    68|build-linux: build-linux-amd64 build-linux-arm64
    69|
    70|# Internal target — do not call directly.
    71|_cross-build:
    72|	@printf '#!/usr/bin/env bash\nargs=()\nfor arg in "$$@"; do case "$$arg" in --unresolved-symbols=*) ;; -Wl,--unresolved-symbols=*) ;; *) args+=("$$arg") ;; esac; done\nexec $(ZIG_BIN) cc -target $(ZIG_TARGET) "$${args[@]}"\n' \
    73|	  > /tmp/zigcc-$(ZIG_TARGET).sh
    74|	@chmod +x /tmp/zigcc-$(ZIG_TARGET).sh
    75|	cd $(ROOT) && CC=/tmp/zigcc-$(ZIG_TARGET).sh CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) \
    76|	  go build -trimpath -buildmode=c-shared \
    77|	  -o sahl/examples/spa/$(OUT) ./sahl/examples/spa/cmd
    78|	@echo "built: $(OUT)"
    79|
    80|# ── Docker ────────────────────────────────────────────────────────────────────
    81|
    82|## docker: cross-compile on host, package into distroless multi-arch image (loads locally)
    83|docker: build-linux
    84|	docker buildx build \
    85|	  --platform linux/amd64,linux/arm64 \
    86|	  --load \
    87|	  -t $(IMAGE_TAG) .
    88|
    89|## docker-push: same as docker but pushes to a registry
    90|## Usage: make docker-push IMAGE_TAG=ghcr.io/dio/luwes/spa:latest
    91|docker-push: build-linux
    92|	docker buildx build \
    93|	  --platform linux/amd64,linux/arm64 \
    94|	  --push \
    95|	  -t $(IMAGE_TAG) .
    96|
    97|## smoke: run the container and verify all endpoints respond correctly
    98|## Builds a local image first if IMAGE_TAG is the default.
    99|smoke: _smoke-build
   100|	@echo "--- starting container..."
   101|	@docker run -d --rm --name spa-smoke -p 10002:10000 $(IMAGE_TAG)
   102|	@trap 'docker stop spa-smoke 2>/dev/null; exit' INT TERM; \
   103|	for i in $$(seq 1 15); do \
   104|	  curl -sf http://localhost:10002/ >/dev/null 2>&1 && break; \
   105|	  sleep 1; \
   106|	done
   107|	@echo "--- GET / (expect 200 HTML)"
   108|	@curl -sf -o /dev/null -w "  %{http_code} %{content_type}\n" http://localhost:10002/
   109|	@echo "--- GET /about (expect 200 HTML, SPA fallback)"
   110|	@curl -sf -o /dev/null -w "  %{http_code} %{content_type}\n" http://localhost:10002/about
   111|	@echo "--- GET /api/hello (expect 200 JSON)"
   112|	@curl -sf http://localhost:10002/api/hello | \
   113|	  python3 -c "import sys,json; d=json.load(sys.stdin); assert d['filter']=='api-backend'; print(' ', d)"
   114|	@echo "--- GET /api/time (expect 200 JSON with timestamp)"
   115|	@curl -sf http://localhost:10002/api/time | \
   116|	  python3 -c "import sys,json; d=json.load(sys.stdin); assert 'T' in d['time']; print(' ', d)"
   117|	@echo "--- GET /api/unknown (expect 404)"
   118|	@curl -sf -o /dev/null -w "  %{http_code}\n" http://localhost:10002/api/unknown || true
   119|	@echo "--- all smoke checks passed"
   120|	@docker stop spa-smoke
   121|
   122|_smoke-build:
   123|	@if [ "$(IMAGE_TAG)" = "spa:latest" ]; then \
   124|	  docker buildx build --platform linux/amd64 --load -t $(IMAGE_TAG) . 2>/dev/null || \
   125|	  docker buildx build --platform linux/arm64 --load -t $(IMAGE_TAG) .; \
   126|	fi
   127|
   128|## clean: remove compiled .so files
   129|clean:
   130|	rm -f $(OUTPUT) $(OUTPUT:.so=.h) libspa.linux-amd64.so libspa.linux-arm64.so
   131|