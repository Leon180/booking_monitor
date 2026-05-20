package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"booking_monitor/internal/application/admin"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/api/sse"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
)

// AdminStreamModule wires the admin event SSE stack into an fx app:
// bus, hub, subscriber, handler, JWT middleware, and lifecycle hooks
// for graceful shutdown.
//
// Usage (in a subcommand's fx.New):
//
//	fx.New(
//	  bootstrap.CommonModule(cfg),
//	  bootstrap.AdminStreamModule,
//	  fx.Invoke(installRoutes), // your handler registration
//	)
//
// The fx.Invoke registers Run() goroutines for the bus/hub/subscriber
// at OnStart and orchestrates the Q15 graceful drain at OnStop:
//
//  1. Handler.SetShuttingDown(true) — new connections receive 503
//  2. Handler.BroadcastRetryHints — jittered retry: hint to all clients
//  3. Cancel runCtx — subscriber stops XREAD, hub drains its broadcast
//     channel, hub closes all client.Send
//  4. Wait for subscriber+hub+bus Done() signals with bounded timeout
//
// fx wires these as singletons; the same instances are injected into
// any consumer that depends on admin.Bus.
var AdminStreamModule = fx.Module("admin_stream",
	fx.Provide(
		// Bus interface (admin.Bus) — provide both the concrete type and
		// the interface satisfaction so callers can depend on either.
		newAdminEventBus,
		fx.Annotate(
			func(b *busAdapter) admin.Bus { return b.bus },
			fx.As(new(admin.Bus)),
		),
		// Hub, Subscriber, Handler — concrete pointers
		newAdminSSEHub,
		newAdminStreamSubscriber,
		newAdminSSEHandler,
		// JWT middleware factory
		newAdminJWTMiddleware,
	),
	fx.Invoke(installAdminStreamLifecycle),
)

// busAdapter holds the concrete adminEventBus pointer so fx can both
// inject the concrete type (for lifecycle wiring) and expose the
// admin.Bus interface (for consumers that should not see the impl).
type busAdapter struct {
	bus *cache.AdminEventBus
}

func newAdminEventBus(client redis.UniversalClient, cfg *config.Config, logger *mlog.Logger) *busAdapter {
	busCfg := cache.DefaultAdminEventBusConfig()
	// Future: thread cfg.AdminStream.* overrides here when the
	// config struct gets dedicated env vars. For now, defaults
	// are the production setting per Q3 design.
	_ = cfg
	bus := cache.NewAdminEventBus(client, busCfg, logger)
	return &busAdapter{bus: bus}
}

func newAdminSSEHub(logger *mlog.Logger) *sse.Hub {
	return sse.NewHub(logger)
}

func newAdminStreamSubscriber(client redis.UniversalClient, hub *sse.Hub, shutdowner fx.Shutdowner, logger *mlog.Logger) *sse.Subscriber {
	cfg := sse.DefaultSubscriberConfig(cache.DefaultAdminStreamKey)
	return sse.NewSubscriber(client, hub, shutdowner, cfg, logger)
}

func newAdminSSEHandler(client redis.UniversalClient, hub *sse.Hub, logger *mlog.Logger) *sse.Handler {
	cfg := sse.DefaultHandlerConfig(cache.DefaultAdminStreamKey)
	return sse.NewHandler(client, hub, cfg, logger)
}

// AdminJWTHandle wraps the gin middleware so fx can inject it as a
// concrete type. Callers (route registration) call .Func() to mount
// it as a per-route guard.
type AdminJWTHandle struct {
	gin gin.HandlerFunc
}

// Func returns the gin.HandlerFunc for route registration.
func (a *AdminJWTHandle) Func() gin.HandlerFunc { return a.gin }

func newAdminJWTMiddleware(cfg *config.Config) *AdminJWTHandle {
	secret := []byte(cfg.AdminStream.JWTSecret)
	if len(secret) == 0 {
		// Fail-closed: empty secret in config → middleware 503s every
		// request. Startup-time validation in production deployments
		// should reject this; the fallback exists so routes register
		// cleanly in dev without requiring a secret.
		return &AdminJWTHandle{
			gin: func(c *gin.Context) {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
					"error": "admin endpoint disabled (no JWT secret configured)",
				})
			},
		}
	}
	maxTTL := cfg.AdminStream.JWTMaxTTL
	if maxTTL == 0 {
		maxTTL = middleware.DefaultAdminJWTMaxTTL
	}
	return &AdminJWTHandle{
		gin: middleware.AdminJWTMiddleware(middleware.AdminJWTConfig{
			Secret: secret,
			MaxTTL: maxTTL,
		}),
	}
}

// installAdminStreamLifecycle wires OnStart / OnStop hooks.
//
// OnStart spawns three goroutines:
//   - bus.Run(runCtx) — drainer
//   - hub.Run(runCtx) — broadcast loop
//   - subscriber.Run(runCtx) — Redis XREAD
//
// OnStop orchestrates the Q15 graceful drain:
//   1. handler.SetShuttingDown(true)
//   2. handler.BroadcastRetryHints — jittered retry: per connected client
//   3. cancel runCtx — signals all three goroutines
//   4. wait for done signals, bounded by stopCtx deadline
func installAdminStreamLifecycle(
	lc fx.Lifecycle,
	bus *busAdapter,
	hub *sse.Hub,
	subscriber *sse.Subscriber,
	handler *sse.Handler,
	logger *mlog.Logger,
) {
	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			logger.Info(runCtx, "admin stream lifecycle: launching bus + hub + subscriber goroutines")
			go bus.bus.Run(runCtx)
			go hub.Run(runCtx)
			go subscriber.Run(runCtx)
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			logger.Info(stopCtx, "admin stream lifecycle: beginning graceful drain")

			// Step 1: reject new connections
			handler.SetShuttingDown(true)

			// Step 2: hint clients to stagger their reconnects
			//   (best effort — broadcast may fail if hub already stopping)
			retryCtx, retryCancel := context.WithTimeout(stopCtx, 100*time.Millisecond)
			handler.BroadcastRetryHints(retryCtx)
			retryCancel()

			// Step 3: cancel runCtx — propagates to subscriber, hub, bus
			cancel()

			// Step 4: wait for done signals with bounded timeout
			//   Subscriber and hub stop first (XREAD interrupted, hub
			//   broadcasts drained); bus drainer flushes remaining
			//   events to Redis with its own DrainTimeout.
			waitDone := func(name string, done <-chan struct{}) error {
				select {
				case <-done:
					return nil
				case <-stopCtx.Done():
					return errors.New(name + " did not stop within fx deadline")
				}
			}
			if err := waitDone("subscriber", subscriber.Done()); err != nil {
				logger.Warn(stopCtx, "admin stream shutdown: subscriber timeout")
				return err
			}
			if err := waitDone("hub", hub.Stopped()); err != nil {
				logger.Warn(stopCtx, "admin stream shutdown: hub timeout")
				return err
			}
			if err := waitDone("bus", bus.bus.Done()); err != nil {
				logger.Warn(stopCtx, "admin stream shutdown: bus timeout")
				return err
			}
			logger.Info(stopCtx, "admin stream lifecycle: graceful drain complete")
			return nil
		},
	})
}
