package observability

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// RegisterRuntimeMetrics opts in to the EXTENDED Go runtime metric set.
//
// The default `prometheus/client_golang` registry already exports basic
// Go metrics (`go_gc_duration_seconds`, `go_goroutines`, `go_memstats_*`,
// `process_*`). What it does NOT export by default is the runtime/metrics
// extended set introduced in Go 1.17:
//
//   - go_sched_latencies_seconds (histogram) — scheduler dispatch
//     latency, the canonical signal for "are goroutines starving"
//   - go_sched_pauses_total_*    — preemption counters
//   - go_cpu_classes_*           — CPU time broken down by class
//     (user/gc/idle/...)
//   - go_memory_classes_*        — finer-grained memory breakdown
//     (heap stacks, metadata, profiling, etc.)
//   - go_gc_heap_*               — per-GC-cycle heap stats
//
// These are the metrics a senior reviewer will look for when diagnosing
// "GC pressure" or "request latency tail dominated by Go runtime, not
// application work". Cost: a single extra collector at /metrics scrape
// time.
//
// Wired explicitly via fx.Invoke from main.go (NOT init()) so the
// registration is grep-able, ordered, and testable. Mirrors the
// pattern used by registerDBPoolCollector for the same reason.
//
// Reference: the `collectors` package in client_golang ≥1.12 supersedes
// the deprecated `prometheus.NewGoCollector()` and
// `prometheus.NewProcessCollector()`. `WithGoCollectorRuntimeMetrics(MetricsAll)`
// matches the full set.
func RegisterRuntimeMetrics() error {
	// Replace the legacy basic Go collector that client_golang's own
	// init auto-registered with the extended one. Unregister first
	// to avoid the AlreadyRegisteredError that Register() returns
	// when the basic collector is still present.
	prometheus.DefaultRegisterer.Unregister(collectors.NewGoCollector())
	if err := prometheus.DefaultRegisterer.Register(
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll),
		),
	); err != nil {
		// AlreadyRegisteredError on a 2nd call (test re-import, fx
		// restart) means the extended collector is already in place
		// from a prior call — that's success, not failure. Other
		// errors (e.g. descriptor conflict with an unrelated collector)
		// remain fatal.
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return fmt.Errorf("RegisterRuntimeMetrics: %w", err)
		}
	}
	return nil
}
