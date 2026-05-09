-- D12.5 — Per-stage Postgres database isolation for the 4-stage
-- comparison harness (PR #108 + this PR).
--
-- The harness's `docker-compose.comparison.yml` (Slice 1) mounts this
-- file at `/docker-entrypoint-initdb.d/02_create_stage_dbs.sql` on the
-- bench Postgres container. The official `postgres:15-alpine` image
-- runs every `*.sql` and `*.sh` under that directory ONCE on first
-- volume initialization, in lexical order — so this file runs after
-- `01_*.sql` (if any) which handles user/role setup.
--
-- The 4 databases (`booking_stage1` through `booking_stage4`) host
-- isolated migration namespaces for each comparison-harness stage so
-- a benchmark run in Stage 1 cannot contaminate Stage 4's data, and
-- vice versa. Stages share the same Postgres SERVER (single container)
-- to keep the bench resource footprint small.
--
-- ## Idempotency note
--
-- Postgres's `docker-entrypoint-initdb.d` only runs init scripts on
-- FIRST volume init (when `PGDATA` is empty). On container restart
-- with the volume intact this script is skipped entirely, so the
-- `IF NOT EXISTS` guard below is technically defensive — but it
-- protects against:
--   1. A maintainer manually re-running this file via `psql -f`
--      against an already-initialized cluster.
--   2. Postgres's `docker-entrypoint-initdb.d` semantics changing
--      in a future image upgrade (unlikely but cheap to guard).
--
-- ## Permission + concurrency assumptions (Slice 0 review)
--
-- This script REQUIRES superuser privileges to issue `CREATE DATABASE`.
-- It runs as `POSTGRES_USER` under docker-entrypoint, which is a
-- superuser by image default. If you ever add an earlier init script
-- (`00_*.sql`) that demotes the active role to a non-superuser, the
-- `\gexec` half of each block below will silently skip the CREATE
-- (zero-row result from `WHERE NOT EXISTS` + permission failure
-- on the implicit catalog read). The verification block at the END
-- of this file converts that silent skip into a hard error.
--
-- The `WHERE NOT EXISTS` guard is NOT atomic with `CREATE DATABASE`.
-- Concurrent `psql -f` invocations on the same cluster could both
-- see "doesn't exist" and both attempt CREATE; one wins, one errors
-- with `42P04 duplicate_database`. This is impossible under
-- `docker-entrypoint-initdb.d` (sequential by contract) but matters
-- if a CI matrix runs this file in parallel. Don't do that.
--
-- Postgres pre-15 does NOT support `CREATE DATABASE IF NOT EXISTS`
-- as standard SQL. The `\gexec` form below works on 13/14/15/16
-- alike and is the canonical idempotent CREATE DATABASE pattern in
-- the Postgres ecosystem.
--
-- ## Why uniform-migration policy
--
-- The orchestration script (Slice 3) applies ALL migrations to ALL
-- 4 stage DBs uniformly, even though Stage 1 doesn't need migration
-- 000010 (saga audit table) and Stages 1-3 don't use migration 000013
-- (saga state widening). The ~10ms per stage of unused-migration cost
-- is far cheaper than the maintenance burden of "Stage 1 has
-- 000007 but not 000010, Stage 2 has both, Stage 3…". Apples-to-
-- apples means same schema everywhere; stages just don't use what
-- they don't need.

SELECT 'CREATE DATABASE booking_stage1'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'booking_stage1')\gexec

SELECT 'CREATE DATABASE booking_stage2'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'booking_stage2')\gexec

SELECT 'CREATE DATABASE booking_stage3'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'booking_stage3')\gexec

SELECT 'CREATE DATABASE booking_stage4'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'booking_stage4')\gexec

-- Post-create verification block. Converts a silently-incomplete
-- create (e.g., role demoted to non-superuser between blocks; a
-- single `\gexec` SELECT erroring out without halting psql) into
-- a hard error visible at init time. The orchestration script
-- (Slice 3) WOULD eventually catch a missing stage DB at
-- migrate-up time, but failing here is closer to the root cause.
DO $$
DECLARE
    n_stage_dbs INT;
BEGIN
    SELECT count(*) INTO n_stage_dbs
      FROM pg_database
     WHERE datname LIKE 'booking_stage%';
    IF n_stage_dbs <> 4 THEN
        RAISE EXCEPTION
          'expected 4 booking_stage* databases after init, found %. partial-create scenario; check superuser permission + earlier init scripts',
          n_stage_dbs;
    END IF;
END $$;
