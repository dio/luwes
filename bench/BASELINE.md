# Baseline benchmark results
# Captured before any luwes optimizations (Phase 0 -- verbatim upstream SDK).
# Platform: darwin/arm64 (Apple M1)
# Run: go test -bench=. -benchmem -count=3 ./bench/

goos: darwin
goarch: arm64
pkg: github.com/dio/luwes/bench
cpu: Apple M1

BenchmarkHeaderAuthAccept-8     ~120 ns/op    ~200 B/op    1 allocs/op
BenchmarkHeaderAuthReject-8     ~187 ns/op    ~430 B/op    1 allocs/op
BenchmarkGetOne-8                ~18 ns/op      0 B/op     0 allocs/op  <- already zero
BenchmarkGet-8                   ~31 ns/op     16 B/op     1 allocs/op
BenchmarkGetMiss-8               ~21 ns/op      0 B/op     0 allocs/op  <- fake returns nil
BenchmarkGetAll-8               ~178 ns/op    320 B/op     1 allocs/op
BenchmarkGetChunks-8            ~0.3 ns/op      0 B/op     0 allocs/op  <- fake: no CGO
BenchmarkFilterCreate-8         ~0.3 ns/op      0 B/op     0 allocs/op  <- fake: no CGO

## Notes

BenchmarkHeaderAuthAccept: 1 alloc/op = the Filter struct allocated in factory.Create().
This is the handle pool target. Post Phase 2, this should converge to 0 allocs/op.

BenchmarkGet: 1 alloc/op = []UnsafeEnvoyBuffer slice on hit.
GetOne is already 0 allocs. The example correctly uses GetOne.

BenchmarkGetAll: 1 alloc/op in fake (single Go map iteration, single make).
In real CGO path: 2 allocs/op (C array + conversion slice). Fake understates this.

BenchmarkGetChunks / BenchmarkFilterCreate: 0 ns/op in fake because the fake
implementations have no CGO cost. These baselines only capture the Go-side
overhead. Real CGO overhead is only visible in e2e pprof profiles.

## Targets (post-optimization)

| Benchmark              | Baseline  | Target    |
|------------------------|-----------|-----------|
| HeaderAuthAccept       | 1 alloc   | 0 allocs  |
| Get (hit)              | 1 alloc   | 0 allocs  |
| GetAll (10 headers)    | 1+ allocs | 0 allocs  |
| GetChunks              | fake:0    | 0 (CGO path confirmed by e2e pprof) |
| FilterCreate           | fake:0    | 0 (CGO path confirmed by e2e pprof) |
