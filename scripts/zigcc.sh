#!/usr/bin/env bash
# scripts/zigcc.sh
# Wraps "zig cc" for use as CGO_CC.
#
# Go's linker passes --unresolved-symbols=ignore-all which LLVM lld (used by
# zig cc) does not support. This wrapper strips that flag before forwarding.
#
# Usage:
#   TARGET=x86_64-linux-gnu CC=$(pwd)/scripts/zigcc.sh CGO_ENABLED=1 go build ...
#
# ZIG env var overrides the zig binary path.
# TARGET env var sets the cross-compile target triple (default: x86_64-linux-gnu).
set -euo pipefail

ZIG="${ZIG:-$(dirname "$(realpath "${BASH_SOURCE[0]}")")/../.bin/zig}"
TARGET="${TARGET:-x86_64-linux-gnu}"

args=()
for arg in "$@"; do
    [[ "$arg" == "--unresolved-symbols="* ]] && continue
    args+=("$arg")
done

exec "$ZIG" cc -target "$TARGET" "${args[@]}"
