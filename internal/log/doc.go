// Package log is the internal logging facade for booking-cli.
//
// It wraps go.uber.org/zap while owning a minimal Logger type so the
// rest of the codebase doesn't depend on zap types directly. Callers
// always hold a *log.Logger (our type), never a *zap.SugaredLogger.
//
// # Why own the type
//
// Owning the surface makes three things easier:
//
//  1. Testing — NewNop() returns a silent logger for unit tests without
//     touching the global zap logger.
//  2. Context propagation — NewContext / FromContext carry a Logger via
//     context.Context the same way kubernetes/klog and stdlib log/slog do.
//  3. Implementation swap — a future migration to log/slog replaces the
//     body of this package without rippling into the 60+ call sites
//     that already use Logger.L(), Logger.S(), Logger.With(), etc.
//
// # Typed tags
//
// Call sites build structured fields with internal/log/tag (e.g.
// tag.OrderID, tag.Error), not raw "key", value pairs. This catches
// typos at compile time and keeps the key vocabulary consistent across
// the codebase. correlation_id and OTEL trace_id / span_id do NOT
// need a tag — they are injected automatically by enrichFields when
// the ctx-aware methods fire.
//
//	log.Error(ctx, "BookTicket failed",
//	    tag.Error(err),
//	    tag.UserID(req.UserID),
//	)
//
// # Usage principles (DI vs context-only)
//
// There are two supported patterns. The split is intentional — not an
// inconsistency. Each pattern puts a different class of field in the
// right place.
//
// Pattern A — DI logger held as a struct field, used by application
// services and long-lived components (*sagaCompensator, *workerService,
// *paymentService, *KafkaConsumer, *SagaConsumer, *redisOrderQueue):
//
//	type sagaCompensator struct { log *mlog.Logger }
//
//	func NewSagaCompensator(..., logger *mlog.Logger) SagaCompensator {
//	    return &sagaCompensator{
//	        log: logger.With(zap.String("component", "saga_compensator")),
//	    }
//	}
//
//	func (s *sagaCompensator) HandleOrderFailed(ctx context.Context, ...) {
//	    s.log.Error(ctx, "failed", tag.OrderID(id))
//	}
//
// Pattern B — package-level ctx-aware calls, used by HTTP handlers,
// the outbox relay, middleware, and init code:
//
//	func (h *bookingHandler) HandleBook(c *gin.Context) {
//	    ctx := c.Request.Context()
//	    log.Error(ctx, "BookTicket failed", tag.Error(err))
//	}
//
// Two classes of field exist, and each pattern handles the right one:
//
//   - Component / subsystem fields (component="saga_compensator",
//     worker_id="w-1") — known at construction, never change →
//     baked into the struct logger via With() ONCE (Pattern A).
//   - Request / call fields (correlation_id, trace_id, span_id,
//     user_id, order_id) — per-request → carried on ctx and auto-
//     injected by enrichFields, or passed as tag.* at the call site.
//
// Choose Pattern A when the caller has a stable component identity
// that belongs on every log line it emits. Choose Pattern B when it
// does not (handlers, middleware, init, background relays that are
// single-instance). This is the convention in Temporal, Cockroach,
// Kubernetes, Grafana, and modern slog codebases — NOT an arbitrary
// mix.
//
// What is NOT a convention here: pure DI (no ctx) loses correlation_id /
// trace_id auto-enrichment. Pure ctx (no struct logger) forces every
// log call to repeat the component name (DRY violation) or adds
// context.WithValue(ctx, "component", ...) boilerplate at every
// method entry.
//
// # Dynamic level
//
// Logger.Level() returns the underlying zap.AtomicLevel so callers can
// change the level at runtime. LevelHandler() returns an http.Handler
// that exposes GET/POST /admin/loglevel — mount it on the pprof
// listener (gated by ENABLE_PPROF) to let oncall flip to debug without
// a restart.
//
// # slog migration (deferred)
//
// Moving to stdlib log/slog is possible but not scheduled. Our Logger
// type already hides zap; switching would replace the body of log.go
// without rippling into call sites (they use L() / S() / With() /
// Error, all of which have direct slog equivalents). Re-evaluate when:
//
//  1. A dependency we need starts shipping only slog handlers, OR
//  2. Prometheus-style dedupe / sampling handlers become needed and
//     slog's Handler interface is the cleanest seam for them.
package log
