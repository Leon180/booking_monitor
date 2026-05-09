//go:build integration

package pgintegration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// redis_harness.go — test-only Redis container for tests that need
// real Lua semantics (Stage 2 deduct_sync.lua, revert.lua's
// idempotency guard, the concurrent-contention assertion). The
// project ships miniredis-backed unit tests in
// `internal/infrastructure/cache/redis_test.go` for fast iteration;
// this harness is the testcontainers counterpart for the cases
// where miniredis's documented Lua-fidelity gaps matter (TTL/EXPIRE
// edges + script invariants).
//
// The harness lives in the pgintegration package because the
// integration tests need both a Postgres container AND a Redis
// container; co-locating them keeps the per-test boot scope
// obvious. A future split (pure-Redis integration tests) can move
// this file then; today the only Redis-using tests also touch PG.

// RedisHarness wraps a live testcontainer + a connected
// `*redis.Client`. Termination is wired through t.Cleanup.
type RedisHarness struct {
	Container testcontainers.Container
	Client    *redis.Client
	Addr      string
}

// StartRedis boots a fresh `redis:7-alpine` container, waits for
// the canonical "Ready to accept connections" log line, and
// returns a RedisHarness with a pre-pinged client. 60s startup
// timeout matches the project's StartPostgres convention.
//
// No password is set: the test container is bound to ephemeral
// host ports + isolated per-test, and a password would just add
// configuration drift between this harness and the docker-compose
// dev stack (which DOES use a password). Production parity is
// covered by live-smoke (`make demo-up`); this is unit-style
// integration, not staging.
func StartRedis(ctx context.Context, t *testing.T) *RedisHarness {
	t.Helper()

	container, err := testcontainers.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("StartRedis: container start: %v", err)
	}

	t.Cleanup(func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(ctx2); err != nil {
			t.Logf("StartRedis: container terminate: %v (non-fatal)", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("StartRedis: host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatalf("StartRedis: mapped port: %v", err)
	}
	addr := fmt.Sprintf("%s:%s", host, port.Port())

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("StartRedis: client.Close: %v (non-fatal)", err)
		}
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		t.Fatalf("StartRedis: ping: %v", err)
	}

	return &RedisHarness{Container: container, Client: client, Addr: addr}
}

// FlushAll clears every key — call in tests that share a Harness
// across subtests to prevent leakage. The container is shared
// (reboot is ~2 s), so a per-test FLUSHALL is the cheap reset.
func (h *RedisHarness) FlushAll(t *testing.T) {
	t.Helper()
	if err := h.Client.FlushAll(context.Background()).Err(); err != nil {
		t.Fatalf("RedisHarness.FlushAll: %v", err)
	}
}
