-- Fixed in CP4c (2026-05-01): the original `DROP TABLE IF NOT EXISTS`
-- was invalid SQL (Postgres only supports `IF EXISTS` on DROP, never
-- `IF NOT EXISTS`). The migration round-trip test in
-- `test/integration/postgres/migration_roundtrip_test.go` surfaced
-- this bug — it had been latent since this migration was first
-- written because no one had ever run the down migration in earnest.
DROP TABLE IF EXISTS events_outbox;
ALTER TABLE events DROP CONSTRAINT IF EXISTS check_available_tickets_non_negative;
