package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	App      AppConfig      `yaml:"app"`
	Server   ServerConfig   `yaml:"server"`
	Redis    RedisConfig    `yaml:"redis"`
	Worker   WorkerConfig   `yaml:"worker"`
	Postgres PostgresConfig `yaml:"postgres"`
	Kafka    KafkaConfig    `yaml:"kafka"`
	Recon    ReconConfig    `yaml:"recon"`
	Saga     SagaConfig     `yaml:"saga"`
}

// ReconConfig holds the tunables for the `recon` subcommand — the
// reconciler that sweeps stuck-Charging orders and resolves them via
// gateway.GetStatus. See internal/application/recon for the loop;
// see PROJECT_SPEC §A4 for the design.
//
// EVERY default below was chosen heuristically — none are backed by
// production data from this codebase yet. Adjust after the first
// production-shaped k6 run produces histograms for:
//
//	recon_charging_resolve_age_seconds — actual age at resolve time
//	recon_gateway_get_status_duration_seconds — actual GetStatus latency
//
// The defaults are deliberately CONSERVATIVE (longer thresholds, fewer
// givings-up) so the system tends toward correctness while we collect
// data.
type ReconConfig struct {
	// SweepInterval is how often the reconciler loop fires when running
	// in default loop mode. Each tick scans for stuck-Charging orders
	// and resolves them.
	//
	// Default 120s rationale: industry standard is "2× provider charge
	// timeout" (Stripe charge timeout is 30s; 2× = 60s); we add another
	// 60s for clock skew between payment-worker and DB. If GetStatus
	// is fast (sub-second) and the gateway is reliable, 120s gives
	// stuck orders ~2 minutes to resolve naturally before recon
	// intervenes — far below SLA-impacting territory.
	//
	// Lower if production data shows recon resolves orders that are
	// significantly older than 120s (lost time-to-recovery). Higher
	// if MockGateway/Stripe call duration trends toward minutes.
	SweepInterval time.Duration `yaml:"sweep_interval" env:"RECON_SWEEP_INTERVAL" env-default:"120s"`

	// ChargingThreshold is the minimum age a Charging order must have
	// before recon considers it "stuck" and queries the gateway. Set
	// HIGHER than the typical successful Pending→Charging→Confirmed
	// duration to avoid stealing orders mid-flight from the worker.
	//
	// Default 120s rationale: same as SweepInterval — typical Stripe
	// p99 charge duration is well under 60s; 120s ensures we never
	// race a normal in-flight Charge call.
	ChargingThreshold time.Duration `yaml:"charging_threshold" env:"RECON_CHARGING_THRESHOLD" env-default:"120s"`

	// GatewayTimeout bounds the per-order GetStatus call. Without a
	// budget, a hung gateway pins the whole sweep loop and blocks
	// SIGTERM-driven shutdown indefinitely.
	//
	// Default 10s rationale: GET /charges/:id at Stripe is a cached,
	// indexed read — typical p99 is well under 1s. 10s gives 10×
	// headroom while keeping the worst case within k8s CronJob
	// terminationGracePeriodSeconds (default 30s).
	GatewayTimeout time.Duration `yaml:"gateway_timeout" env:"RECON_GATEWAY_TIMEOUT" env-default:"10s"`

	// MaxChargingAge is the give-up cutoff. A Charging order older
	// than this is force-transitioned to Failed with reason
	// "max_charging_age_exceeded" and a Prometheus counter increment.
	// Without this, a permanently-Unknown order sweeps forever and
	// the stuck-Charging alert fires with no actionable remediation.
	//
	// Default 24h rationale: payment providers (Stripe, PayPal)
	// typically resolve disputes/manual-review within 24-72h; 24h
	// is short enough that operators are alerted before a customer
	// follow-up window, long enough to absorb provider-side multi-day
	// review states. Tune up to 72h if false-positive give-ups appear.
	MaxChargingAge time.Duration `yaml:"max_charging_age" env:"RECON_MAX_CHARGING_AGE" env-default:"24h"`

	// BatchSize is the maximum number of orders the reconciler resolves
	// per sweep cycle. Caps memory + DB transaction count when a
	// large queue accumulates (e.g., gateway outage drained).
	//
	// Default 100 rationale: at 1 order / ~50ms (one GetStatus + one
	// MarkX), 100 orders = ~5 seconds per sweep. Bounded well below
	// SweepInterval so the next tick can fire on schedule. Bump if
	// production data shows persistent backlog growth.
	BatchSize int `yaml:"batch_size" env:"RECON_BATCH_SIZE" env-default:"100"`
}

// SagaConfig holds the tunables for the `saga-watchdog` subcommand
// (A5) — the sweeper that re-drives stuck-Failed orders through the
// existing (idempotent) compensator. Same shape as ReconConfig but
// distinct so the two sweepers can tune independently:
//
//   - The reconciler queries an EXTERNAL gateway (slow, rate-limited);
//     its tunables reflect gateway latency budget.
//   - The watchdog re-invokes the local compensator (fast, in-process);
//     its tunables can be tighter without affecting external systems.
//
// All defaults are heuristic — same caveat as ReconConfig: tune after
// production data accumulates on `saga_watchdog_resolve_age_seconds`.
type SagaConfig struct {
	// WatchdogInterval — how often the watchdog loop fires. Tighter
	// than the reconciler (60s vs 120s) because the compensator call
	// is local + fast; we'd rather catch stuck-Failed states quickly
	// to keep saga compensations close to real-time customer
	// expectations.
	WatchdogInterval time.Duration `yaml:"watchdog_interval" env:"SAGA_WATCHDOG_INTERVAL" env-default:"60s"`

	// StuckThreshold — minimum age a Failed row must have before the
	// watchdog considers it stuck. Set above the typical
	// Failed→Compensated saga round-trip so we never race the legitimate
	// in-flight compensator.
	//
	// Typical saga compensation is sub-second (Redis revert + DB
	// MarkCompensated); 60s gives 60× headroom and keeps the watchdog
	// conservative. Lower if production shows compensations completing
	// well under the threshold consistently.
	StuckThreshold time.Duration `yaml:"stuck_threshold" env:"SAGA_STUCK_THRESHOLD" env-default:"60s"`

	// MaxFailedAge — give-up cutoff. A Failed row older than this is
	// logged at ERROR + counted under
	// `saga_watchdog_resolved_total{outcome="max_age_exceeded"}` but
	// NOT auto-transitioned (unlike the reconciler's force-fail
	// path) — moving Failed → Compensated without verifying inventory
	// was actually reverted is unsafe. Persistent max-age-exceeded
	// fires the operator alert for manual review.
	//
	// 24h matches the reconciler's MaxChargingAge for symmetry.
	MaxFailedAge time.Duration `yaml:"max_failed_age" env:"SAGA_MAX_FAILED_AGE" env-default:"24h"`

	// BatchSize — max orders to resolve per sweep cycle. 100 mirrors
	// recon. The compensator is faster than gateway.GetStatus, so
	// 100 orders/sweep at <50ms each = ~5s, well within
	// WatchdogInterval. Bump if backlog grows.
	BatchSize int `yaml:"batch_size" env:"SAGA_BATCH_SIZE" env-default:"100"`
}

type AppConfig struct {
	Name     string `yaml:"name" env:"APP_NAME" env-default:"booking_monitor"`
	Version  string `yaml:"version" env:"APP_VERSION" env-default:"1.0.0"`
	LogLevel string `yaml:"log_level" env:"LOG_LEVEL" env-default:"info"`
	WorkerID string `yaml:"worker_id" env:"WORKER_ID" env-default:"worker-1"`
}

type ServerConfig struct {
	Port         string        `yaml:"port" env:"PORT" env-default:"8080"`
	ReadTimeout  time.Duration `yaml:"read_timeout" env:"SERVER_READ_TIMEOUT" env-default:"5s"`
	WriteTimeout time.Duration `yaml:"write_timeout" env:"SERVER_WRITE_TIMEOUT" env-default:"10s"`

	// EnablePprof gates the operator-only pprof listener. Off by default
	// so heap dumps + /admin/loglevel aren't exposed in every deployment.
	EnablePprof bool `yaml:"enable_pprof" env:"ENABLE_PPROF" env-default:"false"`
	// PprofAddr is the bind address for the pprof listener. Defaults to
	// loopback so remote access requires an explicit override.
	PprofAddr         string        `yaml:"pprof_addr" env:"PPROF_ADDR" env-default:"127.0.0.1:6060"`
	PprofReadTimeout  time.Duration `yaml:"pprof_read_timeout" env:"PPROF_READ_TIMEOUT" env-default:"5s"`
	PprofWriteTimeout time.Duration `yaml:"pprof_write_timeout" env:"PPROF_WRITE_TIMEOUT" env-default:"30s"`

	// TrustedProxies is the list of CIDRs Gin trusts for ClientIP()
	// resolution. Default covers RFC1918 ranges (docker / k8s pod
	// CIDRs); override for service meshes with non-RFC1918 IPs (some
	// GKE / EKS setups). In yaml write as a sequence; env vars are
	// parsed as a comma-separated list (env-separator:",").
	TrustedProxies []string `yaml:"trusted_proxies" env:"TRUSTED_PROXIES" env-separator:"," env-default:"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"`
}

type RedisConfig struct {
	Addr         string        `yaml:"addr" env:"REDIS_ADDR" env-default:"localhost:6379"`
	Password     string        `yaml:"password" env:"REDIS_PASSWORD"`
	DB           int           `yaml:"db" env:"REDIS_DB" env-default:"0"`
	PoolSize     int           `yaml:"pool_size" env:"REDIS_POOL_SIZE" env-default:"200"`
	MinIdleConns int           `yaml:"min_idle_conns" env:"REDIS_MIN_IDLE_CONNS" env-default:"20"`
	ReadTimeout  time.Duration `yaml:"read_timeout" env:"REDIS_READ_TIMEOUT" env-default:"500ms"`
	WriteTimeout time.Duration `yaml:"write_timeout" env:"REDIS_WRITE_TIMEOUT" env-default:"500ms"`
	PoolTimeout  time.Duration `yaml:"pool_timeout" env:"REDIS_POOL_TIMEOUT" env-default:"2s"`

	// MaxConsecutiveReadErrors bounds how long the order-stream Subscribe
	// loop tolerates a broken Redis before returning and letting the
	// caller restart the worker. At the current 2s block + 1s sleep
	// cadence, 30 ≈ 90s of persistent failure — long enough to ride out
	// a brief blip, short enough that k8s restarts the pod before the
	// booking backlog becomes unrecoverable. Raise for stricter pods
	// that should self-heal rather than restart; lower for clusters
	// that want faster shedding to a healthy replica.
	MaxConsecutiveReadErrors int `yaml:"max_consecutive_read_errors" env:"REDIS_MAX_CONSECUTIVE_READ_ERRORS" env-default:"30"`

	// InventoryTTL is the maximum lifetime of a `event:{uuid}:qty`
	// inventory key. Long by default (30d) — active events are
	// re-upserted by operational flows (CreateEvent, saga revert)
	// well before expiry; the TTL exists so orphaned keys from deleted
	// events fall off Redis instead of accumulating forever. Lower for
	// tighter memory budgets; raise for events with very long sale
	// windows (festival pre-sale, season-pass).
	InventoryTTL time.Duration `yaml:"inventory_ttl" env:"REDIS_INVENTORY_TTL" env-default:"720h"`

	// IdempotencyTTL bounds how long an `Idempotency-Key`-keyed cached
	// response is retained. Must align with the longest client retry
	// window your callers use; raise for financial reconciliation
	// flows that retry across days.
	IdempotencyTTL time.Duration `yaml:"idempotency_ttl" env:"REDIS_IDEMPOTENCY_TTL" env-default:"24h"`

	// DLQRetention is the bounded retention window for the
	// `orders:dlq` stream. Translated to a Redis Streams MINID
	// directive on every XADD so entries older than NOW-DLQRetention
	// are evicted.
	//
	// Tunable per-environment because retention is a POLICY decision,
	// not a defensive invariant:
	//   * Compliance (PCI / SOX / GDPR) varies by industry
	//   * Operator may want to extend during incident investigation
	//   * Different deployments may have different review SLAs
	//
	// Default 720h (30d) matches typical operator-investigation SLA.
	// Future: pair with S3 archival before MINID drops them — see
	// PROJECT_SPEC §6.8 deferred items.
	DLQRetention time.Duration `yaml:"dlq_retention" env:"REDIS_DLQ_RETENTION" env-default:"720h"`
}

// WorkerConfig holds tunables for the order-stream worker loop
// (`internal/infrastructure/cache/redis_queue.go`). These are pure
// per-environment knobs — the wire-contract values (`streamKey`,
// `groupName`, `dlqKey`, the XADD field names) deliberately stay as
// const because mismatches across replicas would silently split
// brain. See the Const-vs-Config split documented in
// `memory/config_tunables_audit.md`.
type WorkerConfig struct {
	// StreamReadCount is the XReadGroup batch size. Higher = more
	// throughput per call, more memory per goroutine; tune for
	// ingest rate vs. tail latency.
	StreamReadCount int `yaml:"stream_read_count" env:"WORKER_STREAM_READ_COUNT" env-default:"10"`

	// StreamBlockTimeout is how long XReadGroup blocks waiting for
	// new messages before returning empty. Lower = faster shutdown
	// detection, higher CPU; higher = lower CPU, slower shutdown.
	StreamBlockTimeout time.Duration `yaml:"stream_block_timeout" env:"WORKER_STREAM_BLOCK_TIMEOUT" env-default:"2s"`

	// MaxRetries is the per-message retry budget inside
	// `processWithRetry`. Each retry that yields a transient error
	// burns one slot; deterministic-failure errors (NewOrder
	// invariant violations) bypass the budget via the retry policy
	// and route directly to DLQ.
	MaxRetries int `yaml:"max_retries" env:"WORKER_MAX_RETRIES" env-default:"3"`

	// RetryBaseDelay is the base delay for the linear backoff
	// (attempt N waits N*base). Industry would prefer exponential
	// + jitter under heavy concurrency to avoid thundering herd on
	// simultaneous failures; that is a separate refactor.
	RetryBaseDelay time.Duration `yaml:"retry_base_delay" env:"WORKER_RETRY_BASE_DELAY" env-default:"100ms"`

	// FailureTimeout bounds the `handleFailure` compensation budget
	// (Redis revert + DLQ XAdd). Runs against a fresh background
	// context so compensation completes even if the parent ctx was
	// cancelled mid-processing.
	FailureTimeout time.Duration `yaml:"failure_timeout" env:"WORKER_FAILURE_TIMEOUT" env-default:"5s"`

	// PendingBlockTimeout is the XReadGroup block timeout used by
	// the startup PEL recovery sweep. Short so the call honours
	// shutdown signals on cold boot when the stream is empty.
	PendingBlockTimeout time.Duration `yaml:"pending_block_timeout" env:"WORKER_PENDING_BLOCK_TIMEOUT" env-default:"100ms"`

	// ReadErrorBackoff is the sleep between XReadGroup failure
	// retries (NOGROUP / connection drop). Bounds how fast the
	// outer loop re-attempts under persistent breakage.
	ReadErrorBackoff time.Duration `yaml:"read_error_backoff" env:"WORKER_READ_ERROR_BACKOFF" env-default:"1s"`
}

type PostgresConfig struct {
	DSN          string        `yaml:"dsn" env:"DATABASE_URL"` // Constructed or direct override
	MaxOpenConns int           `yaml:"max_open_conns" env:"DB_MAX_OPEN_CONNS" env-default:"50"`
	MaxIdleConns int           `yaml:"max_idle_conns" env:"DB_MAX_IDLE_CONNS" env-default:"5"`
	MaxIdleTime  time.Duration `yaml:"max_idle_time" env:"DB_MAX_IDLE_TIME" env-default:"5m"`
	// MaxLifetime bounds how long a single Postgres connection may live.
	// Unlike MaxIdleTime (which evicts idle conns), MaxLifetime forces
	// recycling of busy conns so long-lived ones don't accumulate
	// staleness (memory, prepared statement caches, PgBouncer auth
	// drift). 30m is a safe default for most deployments.
	MaxLifetime time.Duration `yaml:"max_lifetime" env:"DB_MAX_LIFETIME" env-default:"30m"`

	// PingAttempts / PingInterval / PingPerAttempt bound how long the
	// startup loop waits for Postgres to become reachable. Slower
	// environments (k8s initContainers, spin-up dependencies) should
	// raise PingAttempts; there's no exponential backoff so total budget
	// is roughly PingAttempts * (PingInterval + PingPerAttempt).
	PingAttempts   int           `yaml:"ping_attempts" env:"DB_PING_ATTEMPTS" env-default:"10"`
	PingInterval   time.Duration `yaml:"ping_interval" env:"DB_PING_INTERVAL" env-default:"1s"`
	PingPerAttempt time.Duration `yaml:"ping_per_attempt" env:"DB_PING_PER_ATTEMPT" env-default:"3s"`
}

type KafkaConfig struct {
	// Brokers is the list of Kafka broker addresses. In yaml write as
	// a sequence; env vars are parsed as a comma-separated list
	// (env-separator:",").
	Brokers []string `yaml:"brokers" env:"KAFKA_BROKERS" env-separator:"," env-default:"localhost:9092"`
	// WriteTimeout is the max time to wait for a Kafka write to complete.
	WriteTimeout time.Duration `yaml:"write_timeout" env:"KAFKA_WRITE_TIMEOUT" env-default:"5s"`
	// OutboxBatchSize controls how many outbox events are processed per relay tick.
	OutboxBatchSize int `yaml:"outbox_batch_size" env:"KAFKA_OUTBOX_BATCH_SIZE" env-default:"100"`
	// PaymentGroupID is the Kafka consumer group id for the payment
	// service. Previously hardcoded as "payment-service-group-test"
	// (note the `-test` suffix — a latent prod/test bleed bug).
	PaymentGroupID string `yaml:"payment_group_id" env:"KAFKA_PAYMENT_GROUP_ID" env-default:"payment-service-group"`
	// OrderCreatedTopic is the topic the payment consumer subscribes to.
	// Previously hardcoded.
	OrderCreatedTopic string `yaml:"order_created_topic" env:"KAFKA_ORDER_CREATED_TOPIC" env-default:"order.created"`
	// SagaGroupID is the Kafka consumer group id for the saga consumer.
	// Distinct from PaymentGroupID so the two consumers don't steal
	// messages from each other (they live on different topics anyway
	// but keeping group ids disjoint is defensive).
	SagaGroupID string `yaml:"saga_group_id" env:"KAFKA_SAGA_GROUP_ID" env-default:"booking-saga-group"`
	// OrderFailedTopic is the topic the saga consumer subscribes to.
	OrderFailedTopic string `yaml:"order_failed_topic" env:"KAFKA_ORDER_FAILED_TOPIC" env-default:"order.failed"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	if err := cleanenv.ReadConfig(path, cfg); err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// Validate checks that required-at-startup fields are present. It
// returns the aggregated list of missing fields as one error. Callers
// (cmd/booking-cli) should treat this as fatal and exit before any fx
// wiring runs, so operators get a precise error instead of a cryptic
// connection failure several seconds later.
func (c *Config) Validate() error {
	var missing []string

	// DSN has no env-default: it MUST be set via DATABASE_URL or yaml.
	if strings.TrimSpace(c.Postgres.DSN) == "" {
		missing = append(missing, "postgres.dsn / DATABASE_URL")
	}

	// Server port is defaulted via env-default but a zero-length explicit
	// value would still be invalid.
	if strings.TrimSpace(c.Server.Port) == "" {
		missing = append(missing, "server.port / PORT")
	}

	// Redis / Kafka defaults are fine for local dev, but in production
	// (APP_ENV=production) we reject the localhost defaults so ops can't
	// ship on a silent localhost connection.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
		if isLocalhostAddr(c.Redis.Addr) {
			missing = append(missing, "redis.addr / REDIS_ADDR (localhost default not permitted in production)")
		}
		if isLocalhostBrokers(c.Kafka.Brokers) {
			missing = append(missing, "kafka.brokers / KAFKA_BROKERS (localhost default not permitted in production)")
		}
	}

	// Worker tunables must be positive — a zero-value config (e.g. an
	// operator setting `WORKER_MAX_RETRIES=0` thinking "disable", or a
	// partially-constructed `&config.Config{...}` literal that bypasses
	// cleanenv's env-default tags) makes the worker silently
	// non-functional rather than producing the assumed behaviour:
	//
	//   * MaxRetries=0  → for-loop never iterates → handler never runs → message stuck
	//   * RetryBaseDelay=0 → backoff is instant → fast spin on transient errors
	//   * StreamReadCount=0 → Redis interprets as "all messages" → unbounded batch
	//   * StreamBlockTimeout=0 → busy-poll → CPU pegged
	//   * MaxConsecutiveReadErrors=0 → exit on first error (looks like "disabled")
	//
	// Reject all of these at startup so ops sees the misconfiguration
	// before traffic hits.
	if c.Worker.MaxRetries <= 0 {
		missing = append(missing, "worker.max_retries / WORKER_MAX_RETRIES (must be >= 1)")
	}
	if c.Worker.RetryBaseDelay <= 0 {
		missing = append(missing, "worker.retry_base_delay / WORKER_RETRY_BASE_DELAY (must be > 0)")
	}
	if c.Worker.StreamReadCount <= 0 {
		missing = append(missing, "worker.stream_read_count / WORKER_STREAM_READ_COUNT (must be >= 1)")
	}
	if c.Worker.StreamBlockTimeout <= 0 {
		missing = append(missing, "worker.stream_block_timeout / WORKER_STREAM_BLOCK_TIMEOUT (must be > 0)")
	}
	if c.Worker.FailureTimeout <= 0 {
		missing = append(missing, "worker.failure_timeout / WORKER_FAILURE_TIMEOUT (must be > 0)")
	}
	if c.Worker.PendingBlockTimeout <= 0 {
		missing = append(missing, "worker.pending_block_timeout / WORKER_PENDING_BLOCK_TIMEOUT (must be > 0)")
	}
	if c.Worker.ReadErrorBackoff <= 0 {
		missing = append(missing, "worker.read_error_backoff / WORKER_READ_ERROR_BACKOFF (must be > 0)")
	}
	if c.Redis.MaxConsecutiveReadErrors <= 0 {
		missing = append(missing, "redis.max_consecutive_read_errors / REDIS_MAX_CONSECUTIVE_READ_ERRORS (must be >= 1; 0 silently exits on first error)")
	}
	if c.Redis.DLQRetention <= 0 {
		missing = append(missing, "redis.dlq_retention / REDIS_DLQ_RETENTION (must be > 0; 0 would trim entire DLQ on every XADD)")
	}

	// Recon tunables — same rationale as Worker tunables above. Zero
	// values produce silent reconciler malfunctions:
	//
	//   * SweepInterval=0     → time.Ticker panics
	//   * ChargingThreshold=0 → recon steals every Charging order, even
	//                           in-flight ones the worker hasn't finished
	//   * GatewayTimeout=0    → context.WithTimeout fires instantly →
	//                           every GetStatus call returns ctx error
	//   * BatchSize=0         → SQL LIMIT 0 → silent no-op every sweep
	//   * MaxChargingAge ≤ ChargingThreshold → every order recon sees is
	//                           force-failed before the gateway is queried
	//
	// Reject all of these at startup.
	if c.Recon.SweepInterval <= 0 {
		missing = append(missing, "recon.sweep_interval / RECON_SWEEP_INTERVAL (must be > 0)")
	}
	if c.Recon.ChargingThreshold <= 0 {
		missing = append(missing, "recon.charging_threshold / RECON_CHARGING_THRESHOLD (must be > 0)")
	}
	if c.Recon.GatewayTimeout <= 0 {
		missing = append(missing, "recon.gateway_timeout / RECON_GATEWAY_TIMEOUT (must be > 0)")
	}
	if c.Recon.MaxChargingAge <= 0 {
		missing = append(missing, "recon.max_charging_age / RECON_MAX_CHARGING_AGE (must be > 0)")
	}
	if c.Recon.BatchSize <= 0 {
		missing = append(missing, "recon.batch_size / RECON_BATCH_SIZE (must be >= 1; 0 makes every sweep a silent no-op)")
	}
	if c.Recon.MaxChargingAge > 0 && c.Recon.ChargingThreshold > 0 &&
		c.Recon.MaxChargingAge <= c.Recon.ChargingThreshold {
		missing = append(missing, fmt.Sprintf(
			"recon.max_charging_age (%s) must be > recon.charging_threshold (%s); otherwise every order recon sees is immediately force-failed",
			c.Recon.MaxChargingAge, c.Recon.ChargingThreshold,
		))
	}

	// Saga watchdog (A5) — same guard logic as Recon. Each duration > 0
	// because 0 means "every sweep is a no-op" silently, and the
	// max-age/threshold cross-field invariant prevents force-flagging
	// every visible order on the first sweep.
	if c.Saga.WatchdogInterval <= 0 {
		missing = append(missing, "saga.watchdog_interval / SAGA_WATCHDOG_INTERVAL (must be > 0)")
	}
	if c.Saga.StuckThreshold <= 0 {
		missing = append(missing, "saga.stuck_threshold / SAGA_STUCK_THRESHOLD (must be > 0)")
	}
	if c.Saga.MaxFailedAge <= 0 {
		missing = append(missing, "saga.max_failed_age / SAGA_MAX_FAILED_AGE (must be > 0)")
	}
	if c.Saga.BatchSize <= 0 {
		missing = append(missing, "saga.batch_size / SAGA_BATCH_SIZE (must be >= 1; 0 makes every sweep a silent no-op)")
	}
	if c.Saga.MaxFailedAge > 0 && c.Saga.StuckThreshold > 0 &&
		c.Saga.MaxFailedAge <= c.Saga.StuckThreshold {
		missing = append(missing, fmt.Sprintf(
			"saga.max_failed_age (%s) must be > saga.stuck_threshold (%s); otherwise every order the watchdog sees is immediately flagged max_age_exceeded",
			c.Saga.MaxFailedAge, c.Saga.StuckThreshold,
		))
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}

	return nil
}

// isLocalhostAddr returns true if the string matches the unconfigured
// localhost default cleanenv hands back when REDIS_ADDR is unset.
func isLocalhostAddr(addr string) bool {
	return strings.TrimSpace(addr) == "localhost:6379"
}

// isLocalhostBrokers returns true if the broker slice is the single
// unconfigured localhost default cleanenv hands back when KAFKA_BROKERS
// is unset.
func isLocalhostBrokers(brokers []string) bool {
	return len(brokers) == 1 && strings.TrimSpace(brokers[0]) == "localhost:9092"
}
