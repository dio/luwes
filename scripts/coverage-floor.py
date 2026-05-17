#!/usr/bin/env python3
"""
coverage-floor.py — enforce per-package coverage minimums.

Reads coverage.txt produced by:
  go test -race -coverprofile=coverage.out -covermode=atomic $PKGS 2>&1 | tee coverage.txt

Usage:
  python3 scripts/coverage-floor.py              # uses coverage.txt in cwd
  python3 scripts/coverage-floor.py path/to/coverage.txt

Exit 0 on pass, 1 on any floor violation.
Run locally:
  make coverage   # generates coverage.txt
  python3 scripts/coverage-floor.py
"""
import sys
import re

FLOORS = {
    "github.com/dio/luwes":                              20,
    "github.com/dio/luwes/examples/header-auth":         60,
    "github.com/dio/luwes/examples/hello":               90,
    "github.com/dio/luwes/sahl/examples/auth":           90,
    "github.com/dio/luwes/sahl/examples/decoder":        45,
    "github.com/dio/luwes/sahl/examples/header-auth":    90,
    "github.com/dio/luwes/sahl/examples/spa":            90,
    "github.com/dio/luwes/sahl/examples/sse-tap":        45,
    "github.com/dio/luwes/shared/fake":                  30,
    "github.com/dio/luwes/shared/utility":               60,
}

input_file = sys.argv[1] if len(sys.argv) > 1 else "coverage.txt"

coverage = {}
try:
    with open(input_file) as f:
        for line in f:
            m = re.match(r"ok\s+(\S+)\s+\S+\s+coverage:\s+([\d.]+)%", line)
            if m:
                coverage[m.group(1)] = float(m.group(2))
except FileNotFoundError:
    print(f"error: {input_file} not found — run 'make coverage' first")
    sys.exit(1)

failures = []
for pkg, floor in FLOORS.items():
    pct = coverage.get(pkg)
    if pct is None:
        failures.append(f"MISSING: {pkg} not in coverage output")
    elif pct < floor:
        failures.append(f"LOW: {pkg} = {pct:.1f}% (floor {floor}%)")

if failures:
    for msg in failures:
        print(msg)
    sys.exit(1)

print("coverage check passed")
for pkg, floor in FLOORS.items():
    pct = coverage.get(pkg, 0.0)
    print(f"  {pct:5.1f}%  {pkg}  (floor {floor}%)")
