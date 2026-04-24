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
	Postgres PostgresConfig `yaml:"postgres"`
	Kafka    KafkaConfig    `yaml:"kafka"`
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
