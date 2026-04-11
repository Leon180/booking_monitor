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
}

type KafkaConfig struct {
	// Brokers is a comma-separated list of Kafka broker addresses.
	Brokers string `yaml:"brokers" env:"KAFKA_BROKERS" env-default:"localhost:9092"`
	// WriteTimeout is the max time to wait for a Kafka write to complete.
	WriteTimeout time.Duration `yaml:"write_timeout" env:"KAFKA_WRITE_TIMEOUT" env-default:"5s"`
	// OutboxBatchSize controls how many outbox events are processed per relay tick.
	OutboxBatchSize int `yaml:"outbox_batch_size" env:"KAFKA_OUTBOX_BATCH_SIZE" env-default:"100"`
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
		if isLocalhostAddr(c.Kafka.Brokers) {
			missing = append(missing, "kafka.brokers / KAFKA_BROKERS (localhost default not permitted in production)")
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}

	return nil
}

// isLocalhostAddr returns true if the address string is one of the
// unconfigured localhost defaults cleanenv hands back when nothing
// overrides REDIS_ADDR / KAFKA_BROKERS.
func isLocalhostAddr(addr string) bool {
	a := strings.TrimSpace(addr)
	return a == "localhost:6379" || a == "localhost:9092"
}
