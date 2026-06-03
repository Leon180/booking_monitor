# Chaos engineering runbook — booking_monitor

> PR 9 of the 8+1 PR CI/CD roadmap. Companion to [docs/slo.md](../slo.md).

## What this is

A small, repeatable set of chaos experiments that **verify the [SLOs](../slo.md) hold under failure injection**. The SLO defines what we promise; chaos proves we deliver it under degraded conditions.

## Methodology — hypothesis-first

Chaos engineering is **scientific method applied to running systems** — NOT "random kills to see what breaks." The 4-step loop, per the [Principles of Chaos](https://principlesofchaos.org/) (2017, codified by the Netflix-originated team that built Chaos Monkey in 2010-2011, open-sourced 2012):

1. **State the hypothesis** about steady state ("kill app → /readyz back to 200 within 30s")
2. **Inject failure** as a controlled experiment
3. **Measure** what actually happened
4. **Compare** observation to hypothesis — pass or fail

**Always write the hypothesis BEFORE running the experiment.** Without it, "I killed the container and it broke" is destruction theater, not science.

## Pre-experiment checklist

Before every chaos experiment:

- [ ] SLO error budget healthy (`booking:availability:slo_budget_remaining > 0.5` per [docs/slo.md](../slo.md))
- [ ] Grafana dashboard open, watching `p99` latency + 5xx rate + `/readyz` status
- [ ] Hypothesis written down (which metric, which threshold, which time window)
- [ ] Compose stack at steady state for ≥ 5 minutes (no deploys in flight)
- [ ] Stopwatch / `date -u +%s` to record `t=0` injection time

After:
- [ ] Document result in `docs/chaos-log/<date>-<scenario>.md` (folder created on first experiment)

## Scenarios

All scripts live in [`scripts/chaos/`](../../scripts/chaos/) and are wired into Make targets.

### Scenario A — App kill (`make chaos-kill-app`)

**Hypothesis**: `booking_app` container dying → docker compose `restart: unless-stopped` brings it back within 30s → `/readyz` returns 200. During the kill window, `/livez` external probes get 5xx (acceptable; less than 1 minute → fits within availability SLO's monthly 216 min budget).

**Tool**: `docker restart --timeout=0 booking_app` (SIGKILL the process + restart the container atomically). NOT `docker kill` — see § "Docker kill semantic" below for why.

**Steady-state check**: `curl -fsS http://localhost/readyz` returns 200 (probe via nginx — the `app` service only publishes pprof:6060).

**Recovery target**: `/readyz` returns 200 within 30s of injection.

### Scenario B — Redis kill (`make chaos-kill-redis`)

**Hypothesis**: `booking_redis` dying → `/readyz` returns 503 (correctly reflects dependency loss) → `/api/v1/book` returns 5xx (fails closed, NO silent corruption) → after Redis restart `/readyz` returns 200 again.

**Critical anti-pattern to verify it DOES NOT do**: app continues to accept bookings while Redis is unreachable. That would be **silent inventory corruption** — bookings recorded in DB but inventory not deducted in Redis. The retry-with-backoff in `redis.lua` deduct must NOT short-circuit to "fake success."

**Tool**: `docker kill booking_redis`.

**Recovery target**: `/readyz` back to 200 within 30s of Redis restart.

### Scenario C — Network latency injection (`make chaos-network-latency`)

**Hypothesis**: 500ms artificial latency between `booking_app` and `booking_postgres` → connection pool reuse keeps the per-request latency hit bounded → p99 latency on `POST /api/v1/book` stays under 1500ms (i.e. SLO of 500ms p99 IS broken — but only the latency SLO, not the availability SLO).

**Tool**: `tc qdisc add dev eth0 root netem delay 500ms` inside the app container. Requires `--cap-add NET_ADMIN` on the container (already in docker-compose.yml; verify before running).

**Recovery**: `tc qdisc del dev eth0 root netem` removes the delay; p99 latency returns to baseline.

### Scenario D — Postgres connection saturation (`make chaos-pg-saturation`)

**Hypothesis**: 100 idle PG connections (close to default `max_connections=100`) → app's connection pool retry-with-backoff handles the temporary shortage → booking endpoints return 5xx with `for: 5m+` (briefly) before pool refills; eventually serves again.

**Tool**: 100 parallel `psql -c 'SELECT pg_sleep(120)'` against the DB. `pg_sleep` holds the connection open for the duration; so all 100 connections stay consumed.

**Recovery**: After 120s the pg_sleep calls finish + release connections; pool refills automatically.

### Scenario E — Kafka broker kill (`make chaos-kill-kafka`)

**Hypothesis**: `booking_kafka` dying mid-outbox-publish → outbox events remain `PENDING` in PG (the transactional outbox pattern's core promise) → on Kafka restart, OutboxRelay drains the backlog → all events eventually published exactly once.

**Critical**: this verifies the transactional outbox pattern (PR 1 architecture). If we DID publish to Kafka inside the booking transaction (anti-pattern), this scenario would either lose events or duplicate them. The outbox pattern says: write to DB first, publish later — Kafka downtime is a delay, not a loss.

**Tool**: `docker kill booking_kafka`.

**Recovery target**: After Kafka restart, `outbox_pending_count` returns to ~0 within `OutboxRelay poll interval × 3` (~30s default).

## Make targets

```bash
make chaos-kill-app           # Scenario A
make chaos-kill-redis         # Scenario B
make chaos-network-latency    # Scenario C (run + auto-remove after 60s)
make chaos-pg-saturation      # Scenario D
make chaos-kill-kafka         # Scenario E
```

Each target prints the hypothesis + the metric to watch BEFORE injecting. Operator decides whether to proceed.

## What's deliberately NOT in PR 9

- **chaos-mesh CRDs** — CNCF Incubating (not graduated as I'd initially claimed; [CNCF chaos-mesh](https://www.cncf.io/projects/chaosmesh/) lists status accurately). Wait until k8s migration (PR 8 apply) is done; chaos-mesh is k8s-only.
- **Automated chaos schedule** — game days are operator-driven for now. Adding to CI cron creates risk during job-hunt period when something breaks unattended.
- **Multi-scenario combination tests** — kill Redis AND PG simultaneously, etc. The 5 scenarios above cover the foundation; combinations are exploratory + best done in a dedicated game-day session.

## Docker kill semantic (the live-test gotcha)

When running the chaos scripts against the VM-phase docker compose stack, you might expect `docker kill <container>` + `restart: unless-stopped` to auto-recover the container. **It doesn't.** Empirically verified on Docker 29.4.0:

```
docker kill booking_app          # exit code 137 (SIGKILL)
# 6 minutes later:
docker ps                         # booking_app still missing
docker inspect booking_app        # Exited, Restarting=false
```

Docker treats `docker kill` as an explicit user-initiated stop, even though the restart-policy docs are ambiguous. `unless-stopped` deliberately doesn't restart in this case.

Workarounds in 2026:
- **`docker restart --timeout=0 <c>`** — what `kill_app.sh` uses. SIGKILL + restart in one atomic operation. Doesn't depend on docker's restart-policy interpretation.
- **`docker exec <c> kill -9 1`** — would simulate a "real crash" (process dies, docker sees exit, restart policy fires). DOES NOT WORK on distroless images (no `/bin/kill`), so it's not viable for the booking_monitor app which uses distroless per PR 2.
- **Migrate to k8s + chaos-mesh PodChaos** — k8s does have proper PID-namespace-level kill primitives and triggers Deployment's auto-restart. Tracked in PR 8's migration plan.

`kill_redis.sh` and `kill_kafka.sh` keep using `docker kill` deliberately — those scripts WANT a downtime window to verify fail-closed behavior. They `docker start` the container back up manually after observing the system during the window.

## Tooling alternatives (documented for next-step)

| Tool | When to use | Status (2026-06) |
|---|---|---|
| **chaos-mesh** | K8s deployments; declarative CRDs | CNCF Incubating; active |
| **litmus** | K8s; richer chaos workflow library | CNCF Incubating; active |
| **gremlin** | Commercial; AI-driven fault injection (April 2026 "Reliability Intelligence" feature) | SaaS; paid |
| **toxiproxy** | Service-mesh-style latency / packet-loss injection at TCP level | Shopify; actively maintained |
| **pumba** | Docker-native chaos (kill, pause, network) | Active; v1.0 (2026) added containerd support |
| **shell + docker kill** | Portfolio / VM-phase | This PR's choice |

## Future direction: AI-assisted chaos

Gremlin shipped "Reliability Intelligence" in April 2026 — uses LLMs (via MCP integration) to suggest failure scenarios based on system topology. Emerging trend; not consensus. The hypothesis-first scientific method remains the foundation; AI assists with hypothesis *generation*, not evaluation. Reference for awareness; not adopted in PR 9.

## References

- [Principles of Chaos Engineering (principlesofchaos.org)](https://principlesofchaos.org/)
- [Chaos Monkey history — Gremlin blog](https://www.gremlin.com/chaos-monkey/the-origin-of-chaos-monkey)
- [CNCF chaos-mesh project page](https://www.cncf.io/projects/chaosmesh/)
- [tc netem on Docker — Red Hat Developer](https://developers.redhat.com/articles/2025/05/26/how-simulate-network-latency-local-containers)
- [Toxiproxy (Shopify)](https://github.com/Shopify/toxiproxy)
- [Pumba](https://github.com/alexei-led/pumba)
