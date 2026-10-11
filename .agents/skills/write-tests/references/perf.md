# Performance tests

Rules: [ENGINEERING §2.7](../../../../docs/ENGINEERING.md#27-performance-tests). Worked example: T018 (`pkg/mcp/server`), section "Performance and reliability" in its Notes.

## Benchmark (kept in the repo)

`pkg/mcp/server/bench_test.go`:

```go
func BenchmarkCallTool(b *testing.B) {
	ts := start(b, twoToolsets(b))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() { /* one real HTTP tools/call; b.Error on a non-200 */ }
	})
}
```

- Measure the hot path through the real transport, not a function in isolation.
- `b.ReportAllocs()` always. Use `b.RunParallel` when the code serves concurrent callers.
- Compare before and after with `go test -bench X -count=10 | benchstat old new`. Never assert a duration in a test.

## Load test (results kept, harness thrown away)

1. Run the real server over loopback TCP. Note the machine (cores, and that load and server share it).
2. Use a few concurrency levels (for example 16 / 64 / 512) and the realistic mix (list, call, full client sessions).
3. Record throughput, p50 and p99, errors, heap after GC, and goroutines before and after.
4. Try the failure modes: a cancel storm, clients that never read, overload. Check for leaks and races (`-race` build).
5. Profile CPU and allocations, and say where the cost is, ours or a dependency's.
6. Write the table and the findings in the task's Notes.

## Turning findings into behavior

A limit added because of the load test (call timeout, in-flight cap with `503` + `Retry-After`, session timeout) is a feature. It gets a behavior test (`TestCallTimeout`, `TestMaxInFlight`) and a mutation check, like any other.
