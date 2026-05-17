# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# sahl/spa — distroless Envoy image with the embedded SPA dynamic module.
#
# Build artifacts are produced on the host BEFORE docker build:
#   make build-linux    # cross-compiles libspa.linux-amd64.so + libspa.linux-arm64.so
#
# Then package:
#   docker buildx build --platform linux/amd64,linux/arm64 --load -t spa:latest .
#   # or: make docker
#
# TARGETARCH is injected by BuildKit (amd64 / arm64).
# ─────────────────────────────────────────────────────────────────────────────
FROM envoyproxy/envoy:distroless-v1.38.0

ARG TARGETARCH

# Pre-built by: make build-linux (zig cc cross-compilation from the project root)
COPY libspa.linux-${TARGETARCH}.so /etc/envoy/libspa.so

COPY envoy.yaml /etc/envoy/envoy.yaml

# Required at runtime for Go-based dynamic modules.
ENV GODEBUG=cgocheck=0
ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/etc/envoy

EXPOSE 10000 9901

CMD ["-c", "/etc/envoy/envoy.yaml", "--log-level", "info"]
