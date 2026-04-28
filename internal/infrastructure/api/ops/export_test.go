package ops

import (
	"context"
	"time"
)

// NewHealthHandlerForTest builds a HealthHandler with caller-supplied
// per-dependency ping functions and probe budget — bypasses the
// production constructor so tests don't need a real *sql.DB /
// *redis.Client / Kafka broker, AND can shorten the budget so a
// timeout test doesn't burn 1s of wall clock per CI run.
//
// pings is in deterministic order: results[i].Name == names[i].
// budget=0 falls back to the production default (readinessProbeBudget).
func NewHealthHandlerForTest(names []string, pings []func(ctx context.Context) error, budget time.Duration) *HealthHandler {
	checks := make([]dependencyCheck, len(names))
	for i := range names {
		checks[i] = dependencyCheck{name: names[i], ping: pings[i]}
	}
	if budget <= 0 {
		budget = readinessProbeBudget
	}
	return &HealthHandler{checks: checks, probeBudget: budget}
}
