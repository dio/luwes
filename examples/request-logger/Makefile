ROOT    := $(shell git rev-parse --show-toplevel)
ENVOY   := $(ROOT)/.bin/envoy
DIST    := $(ROOT)/dist
SO      := $(DIST)/librequest-logger.so

.PHONY: all build run server client clean help

all: help

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## build: compile librequest-logger.so for this host OS
build:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-o $(SO) $(ROOT)/examples/request-logger/cmd

## server: start the test backend on 127.0.0.1:11000
server:
	go run $(ROOT)/examples/request-logger/testserver

## run: build, start testserver in background, start Envoy (foreground, Ctrl-C to stop)
run: build $(ENVOY)
	@echo "[request-logger] starting testserver..."
	@go run $(ROOT)/examples/request-logger/testserver &
	@until curl -sf http://127.0.0.1:11000/health >/dev/null 2>&1; do sleep 0.2; done
	@echo "[request-logger] Envoy on :10000 -- access log to stdout"
	@trap 'pkill -f "testserver" 2>/dev/null; exit 0' INT TERM; \
	  GODEBUG=cgocheck=0 \
	  ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(DIST) \
	  $(ENVOY) -c $(ROOT)/examples/request-logger/envoy.yaml --log-level warning; \
	  pkill -f "testserver" 2>/dev/null

## client: send one round of requests through Envoy (Envoy must be running)
client:
	go run $(ROOT)/examples/request-logger/testclient

## clean: remove built .so
clean:
	rm -f $(SO) $(SO:.so=.h)

$(ENVOY):
	$(MAKE) -C $(ROOT) download-envoy
