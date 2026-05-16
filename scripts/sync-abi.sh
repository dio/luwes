#!/usr/bin/env bash
# scripts/sync-abi.sh
# Fetch abi.h from envoyproxy/envoy at a given commit and update abi/VERSION.
#
# Usage:
#   ./scripts/sync-abi.sh                  # use commit from abi/VERSION
#   ./scripts/sync-abi.sh <commit>         # pin to a new commit
#
# After running, review: git diff abi/abi.h
# If ABI_VERSION changed, bump luwes minor version.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
VERSION_FILE="${ROOT}/abi/VERSION"

COMMIT="${1:-}"
if [[ -z "$COMMIT" ]]; then
    COMMIT=$(grep '^ENVOY_COMMIT=' "$VERSION_FILE" | cut -d= -f2)
fi

URL="https://raw.githubusercontent.com/envoyproxy/envoy/${COMMIT}/source/extensions/dynamic_modules/abi/abi.h"

echo "Fetching abi.h from envoy@${COMMIT}..."
curl -fsSL "$URL" -o "${ROOT}/abi/abi.h"

ABI_VER=$(grep '#define ENVOY_DYNAMIC_MODULES_ABI_VERSION' "${ROOT}/abi/abi.h" | grep -oE '"[^"]+"' | tr -d '"')
LINES=$(wc -l < "${ROOT}/abi/abi.h" | tr -d ' ')

# Update VERSION file in-place
sed -i.bak \
    -e "s|^ENVOY_COMMIT=.*|ENVOY_COMMIT=${COMMIT}|" \
    -e "s|^ABI_VERSION=.*|ABI_VERSION=${ABI_VER}|" \
    "$VERSION_FILE"
rm -f "${VERSION_FILE}.bak"

echo "Done."
echo "  Commit:      ${COMMIT}"
echo "  ABI version: ${ABI_VER}"
echo "  Lines:       ${LINES}"
echo ""
echo "Review with: git diff abi/abi.h"
echo "If ABI_VERSION changed, bump luwes minor version in go.mod and tag."
