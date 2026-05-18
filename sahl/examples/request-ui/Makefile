ROOT    := $(shell git rev-parse --show-toplevel)
ENVOY   := $(ROOT)/.bin/envoy
DIST    := $(ROOT)/dist
SO      := $(DIST)/librequest-ui.so
GOARCH  ?= $(shell go env GOARCH)

.PHONY: all build run server client simulate docker-up docker-down clean help

all: help

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## build: compile librequest-ui.so for this host OS
build:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-o $(SO) $(ROOT)/sahl/examples/request-ui/cmd

## server: start the test backend on 127.0.0.1:11000
server:
	go run $(ROOT)/sahl/examples/request-ui/testserver

## run: build, start testserver, start Envoy + UI (foreground, Ctrl-C to stop)
##      UI at http://localhost:6062/
run: build $(ENVOY)
	@echo "[request-ui] starting testserver..."
	@go run $(ROOT)/sahl/examples/request-ui/testserver &
	@until curl -sf http://127.0.0.1:11000/health >/dev/null 2>&1; do sleep 0.2; done
	@echo "[request-ui] Envoy on :10000, UI on http://localhost:6062/"
	@trap 'pkill -f "testserver" 2>/dev/null; exit 0' INT TERM; \
	  REQUI_ADDR=0.0.0.0:6062 \
	  GODEBUG=cgocheck=0 \
	  ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(DIST) \
	  $(ENVOY) -c $(ROOT)/sahl/examples/request-ui/envoy-local.yaml --log-level warning; \
	  pkill -f "testserver" 2>/dev/null

## client: send one round of requests through Envoy -- results appear in UI
client:
	go run $(ROOT)/sahl/examples/request-ui/testclient

## simulate: zero-dependency demo (no Envoy, no Postgres, no Docker)
##           UI at http://localhost:6062/
simulate:
	@go build -o /tmp/requi-simulate $(ROOT)/sahl/examples/request-ui/cmd/simulate/
	REQUI_ADDR=0.0.0.0:6062 /tmp/requi-simulate

## docker-up: build linux .so for this platform then start Docker Compose stack
docker-up:
	@mkdir -p $(DIST)
	TARGET=$(if $(filter arm64,$(GOARCH)),aarch64-linux-gnu.2.28,x86_64-linux-gnu.2.28) \
	CC=$(ROOT)/scripts/zigcc.sh \
	CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) \
	  go build -trimpath -buildmode=c-shared \
	  -o $(DIST)/librequest-ui.linux-$(GOARCH).so \
	  $(ROOT)/sahl/examples/request-ui/cmd
	cp $(DIST)/librequest-ui.linux-$(GOARCH).so $(SO)
	docker compose up

## docker-down: stop the Docker Compose stack
docker-down:
	docker compose down

## clean: remove built artifacts
clean:
	rm -f $(SO) $(SO:.so=.h) \
	      $(DIST)/librequest-ui.linux-*.so \
	      $(DIST)/librequest-ui.linux-*.h \
	      /tmp/requi-simulate

$(ENVOY):
	$(MAKE) -C $(ROOT) download-envoy
