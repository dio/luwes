// Package bench contains baseline benchmarks for luwes SDK allocation profiling.
//
// Run all benchmarks:
//
//	go test -bench=. -benchmem -count=5 ./bench/
//
// Capture a heap profile:
//
//	go test -bench=BenchmarkHandleOnly -benchmem -memprofile=bench/mem.out ./bench/
//	go tool pprof -alloc_objects -http=:8080 bench/mem.out
//
// These benchmarks use the fake SDK (no CGO, no real Envoy). They measure
// the Go-side allocation cost of each operation accurately. The CGO round-trip
// overhead is visible only in the e2e pprof profile under real load.
//
// Baseline numbers (before any luwes optimizations) are captured here and
// compared against post-optimization runs. Every optimization must show a
// measurable delta in allocs/op.
package bench
