# `docs/demo/` — Pattern A terminal walkthrough

A 3-minute asciinema recording of the full Pattern A flow against the running stack — the D10-minimal portfolio piece (per [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) D10).

## What's here

- [`walkthrough.cast`](walkthrough.cast) — asciinema recording of [`scripts/d10_demo_walkthrough.sh`](../../scripts/d10_demo_walkthrough.sh) running against a fresh stack. Plain JSON; commits as text. (Generated artifact — see "Re-recording" below if missing or stale.)

## What the recording shows

Three phases (~3 minutes total):

1. **Phase 1 — happy path** (~30s): `book` → `pay` → `confirm-succeeded` → poll until `paid`. Demonstrates §5 forward recovery: each step has its own retry safety, no compensation needed.
2. **Phase 2 — payment failed** (~30s): `book` → `pay` → `confirm-FAILED` → poll until `compensated`. Demonstrates §4 backward recovery: the D5 webhook payment_failed path triggers saga compensation.
3. **Phase 3 — abandonment** (~60s, mostly the 20s reservation window + sweep tick + saga round-trip): `book` → never call `/pay` → poll until `compensated`. Demonstrates D6 expiry sweeper as the cancellation-trigger source for §4 backward recovery.

The companion blog post [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](../blog/2026-05-saga-pure-forward-recovery.zh-TW.md) ([EN](../blog/2026-05-saga-pure-forward-recovery.md)) explains the architectural rationale these phases demonstrate.

## Replay

```bash
# Install asciinema (one-time, host-side)
brew install asciinema   # macOS; or `pipx install asciinema` cross-platform

# Play the recording
asciinema play docs/demo/walkthrough.cast

# Or play at 2× speed
asciinema play -s 2 docs/demo/walkthrough.cast
```

The `.cast` format is plain JSON — readable in any text editor for spot-inspection.

## Re-recording

When the underlying flow changes (new states, new endpoints, new env semantics), re-record:

```bash
# 1. Bring up the demo stack with 20s reservation + 5s sweep tick
#    (the D10 walkthrough script depends on these timing knobs to keep the
#    abandonment phase under ~60s).
make demo-up

# 2. Verify the stack is healthy (livez + readyz both 200)
curl -sS http://localhost/livez
curl -sS http://localhost/readyz

# 3. Record. The walkthrough script paces itself with PAUSE_MS between
#    phases so the recording reads naturally.
asciinema rec docs/demo/walkthrough.cast \
    --overwrite \
    --command='scripts/d10_demo_walkthrough.sh'

# 4. Replay for a sanity check; trim manually with asciinema-edit if a
#    phase drags. (Most takes are usable on first try given the
#    pacing in the script itself.)
asciinema play docs/demo/walkthrough.cast
```

`--overwrite` allows re-recording over an existing file. Without it, asciinema refuses to overwrite to avoid accidental clobber.

### Pacing knobs

The walkthrough script honors `PAUSE_MS` (default `1500`) for natural between-step pauses. To re-record faster (e.g., for CI or quick verification), set `PAUSE_MS=0`. The pause matters mostly for human-watched recordings; CI runs of the walkthrough script itself should always set `PAUSE_MS=0`.

### API origin

The walkthrough defaults to `API_ORIGIN=http://localhost` (nginx on host port 80, the only host-published surface in `docker-compose.yml`). Override `API_ORIGIN=http://localhost:8080` only when running the Go binary directly on the host (`make run-server`) instead of via Docker compose — the `app` service in compose publishes pprof on `:6060` only, NOT `:8080`, so a walkthrough targeting `:8080` from the host would fail to connect.

## Optional: upload to asciinema.org

For embedding in external portfolio sites (resume, personal page), upload to the public service:

```bash
asciinema upload docs/demo/walkthrough.cast
# returns a URL like https://asciinema.org/a/<id>
```

Then add the URL to the README's terminal-walkthrough section so external readers can play in-browser without cloning the repo. The `.cast` file in this directory is the source of truth; the asciinema.org copy is a convenience.

## Why no zh-TW pair for this README

`docs/demo/` is not in the [bilingual contract pair list](../../.claude/CLAUDE.md). EN-only is the convention for tooling READMEs (mirrors `docs/blog/notes/` and `docs/benchmarks/` precedent). The blog post itself, which is paired EN+zh-TW, is the canonical bilingual portfolio piece for the architecture this demo demonstrates.
