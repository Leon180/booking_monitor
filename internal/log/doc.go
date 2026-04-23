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
// the codebase.
//
//	log.FromContext(ctx).L().Error("BookTicket failed",
//	    tag.Error(err),
//	    tag.UserID(req.UserID),
//	)
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
