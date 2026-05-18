ROOT  := $(shell git rev-parse --show-toplevel)
ENVOY := $(ROOT)/.bin/envoy
DIST  := $(ROOT)/dist
NAME  := hello
SO    := $(DIST)/lib$(NAME).so

.PHONY: all build run test clean help

all: help

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## build: compile libhello.so for this host OS
build:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
		-o $(SO) $(ROOT)/examples/hello/cmd

## run: build and start Envoy (foreground, Ctrl-C to stop)
run: build $(ENVOY)
	GODEBUG=cgocheck=0 \
	ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(DIST) \
	$(ENVOY) -c $(ROOT)/examples/hello/envoy.yaml --log-level warning

## test: run unit tests (no Envoy required)
test:
	go test -race -v $(ROOT)/examples/hello/...

## clean: remove built .so
clean:
	rm -f $(SO) $(SO:.so=.h)

$(ENVOY):
	$(MAKE) -C $(ROOT) download-envoy
