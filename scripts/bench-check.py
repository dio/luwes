#!/usr/bin/env python3
"""
bench-check.py — enforce per-benchmark alloc ceilings.

Reads bench/results.txt produced by:
  go test -bench=. -benchmem -count=5 ./bench/ | tee bench/results.txt

Usage:
  python3 scripts/bench-check.py                   # uses bench/results.txt in cwd
  python3 scripts/bench-check.py path/to/results.txt

Exit 0 on pass, 1 on any ceiling violation.
Run locally:
  make bench   # generates bench/results.txt
  python3 scripts/bench-check.py
"""
import sys
import re

# allocs/op ceilings per benchmark name (prefix, stripped of -N suffix).
# fake path values — CGO path is strictly lower. Ceilings are set to the
# fake numbers so local runs pass without Envoy.
CEILINGS = {
    "BenchmarkHeaderAuthAccept": 1,   # fake: var key escapes via interface; CGO: 0
    "BenchmarkGetOne":           0,
    "BenchmarkGetOneInto":       0,
    "BenchmarkGetMiss":          0,
    "BenchmarkGetChunks":        0,
    "BenchmarkHeaderAuthReject": 2,
    "BenchmarkGet":              2,
    "BenchmarkGetAll":           2,
    "BenchmarkFilterCreate":     2,
}

input_file = sys.argv[1] if len(sys.argv) > 1 else "bench/results.txt"

failures = []
seen = set()

try:
    with open(input_file) as f:
        for line in f:
            m = re.match(
                r"(Benchmark\w+)-\d+\s+\d+\s+[\d.]+\s+\S+\s+[\d.]+\s+B/op\s+(\d+)\s+allocs/op",
                line,
            )
            if not m:
                continue
            name, allocs = m.group(1), int(m.group(2))
            seen.add(name)
            if name in CEILINGS and allocs > CEILINGS[name]:
                failures.append(
                    f"REGRESSION: {name} got {allocs} allocs/op, ceiling is {CEILINGS[name]}"
                )
except FileNotFoundError:
    print(f"error: {input_file} not found — run 'make bench' first")
    sys.exit(1)

if failures:
    for msg in failures:
        print(msg)
    sys.exit(1)

print("alloc check passed")
for name, ceiling in CEILINGS.items():
    marker = "(not in results)" if name not in seen else ""
    print(f"  ceiling {ceiling}  {name}  {marker}")
