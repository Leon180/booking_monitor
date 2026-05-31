# Release pipeline runbook

End-to-end walkthrough of the supply-chain-hardened publish pipeline introduced in PR 3. By the time you finish this runbook, every image pushed to Artifact Registry will be:

- Scanned for HIGH/CRITICAL CVEs (Trivy gate; publish fails if any are found)
- Attested with SLSA L3 build provenance (via `actions/attest-build-provenance`)
- Bundled with a SPDX SBOM (via Syft + cosign attest keyless)
- Signed by ephemeral keys with a cert SAN bound to the exact workflow that built it

## One-time setup

### Step 0 — Prerequisites

Confirm these BEFORE Step 1; skipping any causes Step 1 to fail in confusing ways.

```bash
# 1. gcloud authenticated AND ADC set (both required — Terraform uses ADC,
#    gcloud CLI uses regular login)
gcloud auth login
gcloud auth application-default login

# 2. Account has roles/resourcemanager.projectCreator on the billing
#    account/org. Personal accounts have this by default; org-managed
#    accounts may need admin to grant.

# 3. Billing account active + has a payment method
gcloud beta billing accounts list

# 4. Project + state bucket already created (see docs/runbooks/gcp_bootstrap.md
#    Steps 1+2). `make init` below CANNOT bootstrap its own backend.
gcloud projects describe booking-monitor-sandbox    # expect: ACTIVE
gcloud storage buckets describe gs://booking-monitor-sandbox-tfstate \
  --format='value(versioning_enabled)'              # expect: True

# 5. terraform >= 1.11 installed
terraform version

# 6. deploy/terraform/terraform.tfvars filled in, INCLUDING
#    github_owner_id + github_repo_id (numeric — see gcp_bootstrap.md Step 1.5)
```

If any of these fail, fix that first. The rest of this runbook assumes them.

### Step 1 — Apply PR 1's Terraform to your GCP sandbox project

The release pipeline pushes to Artifact Registry + impersonates `sa-ci-deploy` via Workload Identity Federation. Both are provisioned by PR 1's Terraform but not yet applied to any project. Apply now.

```bash
cd deploy/terraform
make verify        # sanity-check the .tf files compile + pass static checks
make init          # init backend against your sandbox project's state bucket
make plan          # eyeball the ~41 resources
make apply         # 60-90 seconds; first apply has some API-enable retries
```

If apply errors with `API not enabled`, wait 30 seconds and re-run — GCP's API enable propagation is asynchronous (see runbook Step 3 in `gcp_bootstrap.md`).

Confirm the outputs were materialized:

```bash
terraform output workload_identity_provider
# Example: projects/872010303436/locations/global/workloadIdentityPools/pool-github/providers/prov-github

terraform output ci_deploy_sa_email
# Example: sa-ci-deploy@booking-monitor-sandbox.iam.gserviceaccount.com

terraform output artifact_registry_url
# Example: us-central1-docker.pkg.dev/booking-monitor-sandbox/booking
```

### Step 2 — Populate Secret Manager values

The release pipeline doesn't read secrets directly, but PR 4/5 will. While Terraform's at this point, fill the empty secret containers (see [`gcp_bootstrap.md`](gcp_bootstrap.md) Step 4 for the full loop).

### Step 3 — Set GitHub Actions repository variables

The release workflow consumes 3 GHA repo **variables** (not secrets — they're just identifiers):

```
Settings → Secrets and variables → Actions → Variables tab
```

| Variable | Value (from terraform output) |
| --- | --- |
| `GCP_WORKLOAD_IDENTITY_PROVIDER` | `terraform output workload_identity_provider` |
| `GCP_CI_DEPLOY_SA` | `terraform output ci_deploy_sa_email` |
| `GCP_ARTIFACT_REGISTRY` | `terraform output artifact_registry_url` |

Verify via `gh variable list` (if you have `gh` CLI authenticated):

```bash
gh variable list
# Should show all three above with non-empty values.
```

### Step 4 — Dry-run with a release candidate tag

Push a test tag to confirm the full chain works before declaring v1.0.0 ready:

```bash
git tag -a v0.0.0-rc1 -m "Release pipeline dry-run"
git push origin v0.0.0-rc1
```

Watch the GH Actions `release` workflow run. Expected outcome:

1. `build + scan + push` job — ~3-5 min — green
2. `attest (SLSA L3 + SBOM)` job — ~2-3 min — green, prints `✓ SLSA provenance verified` and `✓ SBOM attestation verified`

Verify locally:

```bash
export GCP_ARTIFACT_REGISTRY=$(cd deploy/terraform && terraform output -raw artifact_registry_url)
make verify-image VERIFY_IMAGE=${GCP_ARTIFACT_REGISTRY}/booking-monitor:v0.0.0-rc1
```

If both attestations verify, the release pipeline is wired correctly. Cleanup — three places to delete (git tag locally, git tag remote, AR tags). **Do not skip the AR cleanup** — leaving `:latest` pointing to a dry-run image means PR 5's first deploy will deploy the dry-run, not whatever real release you push next.

```bash
# Git tag (local + remote)
git tag -d v0.0.0-rc1
git push origin :refs/tags/v0.0.0-rc1

# Artifact Registry tags (delete ALL three the workflow created)
gcloud artifacts docker tags delete \
  "${GCP_ARTIFACT_REGISTRY}/booking-monitor:v0.0.0-rc1" --quiet
gcloud artifacts docker tags delete \
  "${GCP_ARTIFACT_REGISTRY}/booking-monitor:latest" --quiet
# `:sha-<short>` is fine to leave — it's traceable to a specific commit,
# and the AR cleanup policy (keep last 10 versions) will reap it eventually.
```

## Day-to-day usage

Every push to `main` automatically:
- Builds an image tagged `:main` + `:sha-<7-char-sha>`
- Trivy-scans
- Publishes to Artifact Registry
- Signs + attests provenance + SBOM

Every push of a `v*.*.*` tag adds `:<version>` + `:latest` tags (in addition to the sha tag).

## Cutting a release with release-please (PR 6)

Why release-please vs hand-tagging: release-please derives the next SemVer from commit history (no human guessing PATCH vs MINOR vs MAJOR), keeps CHANGELOG in lockstep, and creates the GH Release in one merge. The author of every PR signals intent via the PR title (`feat:` / `fix:` / `BREAKING CHANGE:`); the bot composes the release.

Day-to-day, you don't manually tag. Workflow:

1. **Write PRs with [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) titles.** Squash-merge means only the PR title becomes the commit on `main`, so only the title needs to parse. [.github/workflows/pr-title-check.yml](../../.github/workflows/pr-title-check.yml) blocks merge on non-conformant titles via `amannn/action-semantic-pull-request@v5`.

   Bump mapping:
   - `feat(api): add /events search endpoint` → **MINOR** (new functionality, backward-compat)
   - `fix(sse): prevent goroutine leak on client disconnect` → **PATCH** (bug fix)
   - `perf(worker): batch Redis pipeline reads` → **PATCH** + appears under "Performance" CHANGELOG section
   - `refactor(api): split bookings handler` → **PATCH** + appears under "Refactoring"
   - `revert(api): revert /events search endpoint` → **PATCH** + appears under "Reverts"
   - **MAJOR** has two equivalent triggers, EITHER is sufficient (not both required):
     - `feat(api)!: remove POST /book legacy endpoint` (the `!` after the scope)
     - OR a `BREAKING CHANGE: callers of /book get 410 Gone` footer in the body
   - `chore(deps): bump golangci-lint to v2.5.0` → no bump, hidden from CHANGELOG
   - `docs(monitoring): add SSE alert recipe` → no bump, hidden

   What counts as breaking is a real engineering call: env var rename = breaking (operator setup changes), Docker base image bump = chore, metric label change = NOT breaking (old scrapers still work, new label is ignored), removing an HTTP endpoint = breaking.

2. **The release-please bot opens or updates a release PR after every push to main.** Title is `chore(release): release vX.Y.Z`, body shows the proposed CHANGELOG diff. The PR stays open and accumulates commits until you merge it.

3. **When you're ready to ship, review + merge the release PR.** release-please then:
   - Squashes the release PR onto `main` (the CHANGELOG.md + `.release-please-manifest.json` updates)
   - Creates the `vX.Y.Z` git tag pointing at the merge commit
   - Creates a GitHub Release with the CHANGELOG body

4. **The tag push fires release.yml + deploy.yml in parallel.** Both watch `push: tags: ['v*']`:
   - `release.yml` builds + signs + pushes the image to AR + attests provenance + SBOM (~4-7 min total: build job 3-5 min, attest job 1-2 min)
   - `deploy.yml` resolves the digest + verifies attestation + deploys to the VM. Has three retry-with-backoff loops (digest lookup ×36 = 6 min, gh attestation verify ×18 = 3 min, cosign verify ×18 = 3 min) to absorb race-with-release.yml.

   This parallel-fan-out + explicit-polling pattern is what real production teams use (verified against [Grafana release-build.yml](https://github.com/grafana/grafana/blob/main/.github/workflows/release-build.yml), [Cosign github-oidc.yaml](https://github.com/sigstore/cosign/blob/main/.github/workflows/github-oidc.yaml), HashiCorp providers). `workflow_run` chaining has 3-40 min latency variance and is actively avoided.

5. **Verify the release worked.** Quick smoke after the workflow runs go green:

   ```bash
   gh release view v1.2.0                                       # CHANGELOG body rendered
   gh run list --workflow=release.yml --limit 1                 # build/sign green
   gh run list --workflow=deploy.yml --limit 1                  # deploy green
   gh api /repos/Leon180/booking_monitor/deployments?environment=production --jq '.[0].statuses_url' | xargs gh api | jq '.[0].state'  # → "success"
   make verify-image VERIFY_IMAGE=$(gh api /repos/Leon180/booking_monitor/releases/tags/v1.2.0 --jq '.body' | grep -oP 'digest: \K[^ ]+' | head -1)
   curl -fsS "https://${PUBLIC_HOST}/livez" && curl -fsS "https://${PUBLIC_HOST}/readyz"
   ```

### Helping yourself write Conventional titles

- **VSCode**: install the "Conventional Commits" extension (vivaxy) for guided PR title composition
- **GH Action**: [.github/workflows/pr-title-check.yml](../../.github/workflows/pr-title-check.yml) blocks merge on non-conformant titles
- **Reference**: keep the bump-mapping table above bookmarked while learning the conventions

### What if the release PR is wrong?

- **Wrong version**: edit `.release-please-manifest.json` on the release PR branch (the bot's branch is `release-please--branches--main`). Push the edit; bot reconciles on next push.
- **Wrong CHANGELOG entry on the bot's branch**: directly edit `CHANGELOG.md` on the release PR branch. The bot preserves edits to its OWN branch between runs. (It does NOT preserve edits made to `main` directly — only its branch.)
- **Don't want to release yet**: leave the PR open. It accumulates further commits until you're ready.
- **Want a different commit to be the bump trigger**: not directly supported; close the PR + push a commit with the right Conventional type, bot opens a fresh PR.

### Pre-release tags (`v1.0.0-rc1`)

Manually edit `.release-please-manifest.json` on the release PR branch to `"1.0.0-rc1"`. The bot tags it that way on merge. `deploy.yml`'s tag regex accepts SemVer pre-release + build metadata (`v1.0.0-rc1`, `v1.0.0+build.5`).

### `[Unreleased]` section gotcha

Our `CHANGELOG.md` keeps an `## [Unreleased]` section at the top (per [Keep a Changelog](https://keepachangelog.com/) convention) listing post-v1.1.0 deferred items. **release-please does NOT understand the `[Unreleased]` convention**. After every release-please run, the new `## [X.Y.Z]` entry is prepended above the existing content, pushing `[Unreleased]` DOWN below the new release entry. This inverts the Keep-a-Changelog ordering.

**Operator action required after every release PR merge**: manually edit `CHANGELOG.md` on `main` to move the `[Unreleased]` section back to the top, above the new `[X.Y.Z]` entry. This is a 30-second edit, but it's necessary or the changelog progressively decays.

Long-term alternatives (not adopted yet, tracked here):
- Drop `[Unreleased]` from CHANGELOG entirely, track post-release items as GH Issues with a `deferred` label
- Use release-please's [sentinel-comment pattern](https://github.com/googleapis/release-please/blob/main/docs/customizing.md) to anchor where entries get inserted

### Recovery: stuck `untagged release PR` state

If release-please's workflow log shows:

```
⚠ There are untagged, merged release PRs outstanding - aborting
```

it means the bot's release PR was merged but the corresponding `vX.Y.Z` tag was never created. The bot refuses to act until the state is reconciled. Every subsequent push will abort the same way until the tag exists.

Three causes seen in practice:
- The release PR was squash-merged (the bot may have lost track of its labels — though our v5 config + labels should usually survive squash)
- `pull-request-title-pattern` in `release-please-config.json` didn't match the actual title format the bot opened the PR with (post-PR #142 fix: we no longer customize the pattern; library default is the only source of truth, eliminating this class of bug)
- The bot's GITHUB_TOKEN doesn't have `contents: write` (verify: `gh api /repos/$OWNER/$REPO/actions/permissions/workflow`)

**Recovery (one-time, operator runs from laptop):**

```bash
# 1. Identify the release PR (it'll be CLOSED + MERGED, title 'chore: release main' or similar)
gh pr list --state merged --search "is:pr in:title 'chore: release'" --limit 1

# 2. Find the merge commit + the version the PR was for
MERGE_SHA=$(gh pr view <number> --json mergeCommit --jq '.mergeCommit.oid')
VERSION=$(jq -r '."."' .release-please-manifest.json)   # what main currently claims is "released"

# 3. Create the release manually. Operator-authenticated git push triggers
#    release.yml + deploy.yml; bot-authenticated pushes do not.
gh release create "v${VERSION}" \
  --target "${MERGE_SHA}" \
  --title "v${VERSION}" \
  --notes "$(gh pr view <number> --json body --jq '.body')"
```

After this, the next push to `main` runs release-please cleanly — the `untagged outstanding` abort is gone because v1.2.0 now exists in both manifest AND git. Subsequent release PRs will propose v1.2.1 / v1.3.0 etc. normally.

**Don't use `git tag` for this.** `gh release create` is preferred because it creates the tag AND the GitHub Release in one atomic step. Bare `git tag` leaves the GH Release missing, which is its own confusing state.



When the release PR merge tags `v1.2.0`, the **tag push event** is created by `${{ secrets.GITHUB_TOKEN }}`. Per [GitHub Actions docs](https://docs.github.com/en/actions/concepts/security/github_token): *"events triggered by the GITHUB_TOKEN will not create a new workflow run."* This is a token-identity restriction (any event from the GH-Actions token is silently dropped from sibling-workflow triggering). So `release.yml` + `deploy.yml` won't auto-fire on the bot's tag push.

Three workarounds, in order of how well they scale:

1. **GitHub App installation token** (recommended in 2026) — install a GH App with `contents: write` + `pull-requests: write` + `actions: write`, use `actions/create-github-app-token@v1` to mint a short-lived token in the workflow, pass that as `token:` to release-please-action. No 90-day rotation, auditable, scoped. Setup is one-time + ~10 lines of YAML. [GH Apps docs](https://docs.github.com/en/apps).

2. **Personal Access Token (PAT)** — fine-grained PAT scoped to this repo, with `contents: write` + `pull-requests: write` + `workflows: write`. Stored as `RELEASE_PLEASE_PAT` secret. Rotate every 90 days (calendar entry recommended). Quicker setup than a GH App, higher long-term cost.

3. **Operator manual tag re-push** — for the period before GH App / PAT is provisioned. After the release PR merges (and tags v1.2.0 silently), the operator runs:
   ```bash
   git fetch --tags
   git push origin v1.2.0    # re-pushes the same tag; authenticated as YOU, not the bot
   ```
   **Do NOT use `--force` / `--force-with-lease`** — the tag already points at the right commit, no force needed. This is a workaround until #1 or #2 is set up, not a long-term pattern.

### v1.1.0 retroactive tag (one-time setup migration — DO NOT REPEAT)

PR 6 setup tagged `v1.1.0` retroactively at PR #118's merge commit (`32c78e4`) because the CHANGELOG had documented this release but the git tag was missed at close-out. The manifest entry `{".":"1.1.0"}` is what anchors release-please's "starting point"; `bootstrap-sha` is a fallback for projects without a manifest entry (verifiable via [release-please's manifest-releaser docs](https://github.com/googleapis/release-please/blob/main/docs/manifest-releaser.md)).

**This was a one-time bootstrap migration. Future versions MUST be tagged exclusively by the release-please bot — never run `git tag -a v*` by hand again.** Hand-tagging breaks the bot's accounting and produces tag/CHANGELOG drift like the one we just spent PR 6 cleaning up.

First release-please run will propose `v1.2.0` (because there are `feat:` commits in PRs #132-#137) or `v1.1.1` (if only `fix:` commits parse since v1.1.0). The bot logs unparseable commit subjects and continues — non-Conventional historical commits don't break the run, they just don't contribute to bump computation.

## Verify any image — `make verify-image`

```bash
# Default: verifies the image tagged sha-<current-HEAD>
make verify-image

# Specific tag
make verify-image VERIFY_IMAGE=us-central1-docker.pkg.dev/.../booking-monitor:v1.2.3

# By digest (recommended for prod deploys — immutable)
make verify-image VERIFY_IMAGE=us-central1-docker.pkg.dev/.../booking-monitor@sha256:abc...
```

Behind the scenes this runs `cosign verify-attestation` twice — once for SLSA provenance, once for SBOM — with the workflow identity regex pinned to `release.yml@refs/heads/main` OR `release.yml@refs/tags/v*`. Any image pushed by any other workflow / branch / fork will fail the check.

## Inspect the SBOM contents

```bash
make verify-image-sbom VERIFY_IMAGE=<...>
# Pipes the SBOM attestation through jq for human reading.
```

## Trivy CVE gate — what to do when it fails

Release pipeline fails with Trivy output listing HIGH/CRITICAL CVEs. Two paths:

1. **Fix the CVE**: update the affected dependency (`go.mod` / `go.sum`), commit, push, the next workflow run won't have the CVE.

2. **Accept temporarily**: add the CVE id to a `.trivyignore` file at repo root. Trivy reads this and excludes listed CVEs. Document WHY in a comment next to each entry — drift between `.trivyignore` entries and actual code reality is how supply chain incidents stay unfixed.

```
# .trivyignore
# CVE-2026-XXXXX — affects k8s.io/client-go transitive in tests only;
#                  not reachable from production code path. Re-evaluate
#                  when v0.30 lands.
CVE-2026-XXXXX
```

## Threat model — what this pipeline protects against

| Threat | Mitigation in pipeline |
| --- | --- |
| Image tampered with after push | cosign signature + Rekor transparency log — any modification breaks signature |
| Image published from forked PR | Trust condition `--certificate-identity-regexp` only accepts `main` + `v*` tag refs; fork PR builds fail verify |
| Image with known HIGH CVE published | Trivy gate runs BEFORE push — fails workflow before image hits AR |
| Provenance forged (claim image came from main) | SLSA L3 attestation signed by GitHub's isolated runner; attacker can't reach the signing key |
| SBOM tampered with | cosign attest signs the SBOM; tamper breaks signature |
| Long-lived service-account key leaked | None to leak — WIF + OIDC, ~15-min tokens |

## Threat model — gaps (covered by later PRs)

- ~~Image signing identity verification at deploy time~~ → **closed by PR 5** ([.github/workflows/deploy.yml](../../.github/workflows/deploy.yml)) — verifies via both `gh attestation verify` (canonical GitHub-native path) AND `cosign verify` (cross-check) before SSH-ing into the VM. See [`deploy.md` § CI-driven deploy](deploy.md#ci-driven-deploy-pr-5).
- Cosign signature replication to mirror for offline verify → not planned
- Trivy ignore-file rot detection (entries that should have been removed) → not planned; manual review

## Cosign + Syft installation (one-time on operator machine)

```bash
# macOS
brew install cosign syft

# Or via Go
go install github.com/sigstore/cosign/v2/cmd/cosign@latest
go install github.com/anchore/syft@latest
```

## Pipeline architecture diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                  GH Actions release workflow                     │
│                                                                  │
│  push main / push v*.*.*                                         │
│        │                                                         │
│        ▼                                                         │
│   ┌─────────┐                                                    │
│   │  build  │  WIF auth → docker build → trivy → docker push    │
│   └────┬────┘                                                    │
│        │ digest                                                  │
│        ▼                                                         │
│   ┌─────────┐                                                    │
│   │ attest  │  actions/attest-build-provenance@v4 → SLSA L3     │
│   │         │  syft → cosign attest → SBOM signed                │
│   │         │  cosign verify-attestation (smoke test)            │
│   └────┬────┘                                                    │
│        │                                                         │
└────────┼─────────────────────────────────────────────────────────┘
         ▼
  ╔═════════════════════════╗      ╔══════════════════════════╗
  ║ GCP Artifact Registry   ║      ║ Sigstore Rekor (public)  ║
  ║  - image                ║      ║  - inclusion proofs       ║
  ║  - SLSA provenance      ║      ║  - signing audit trail    ║
  ║  - SBOM attestation     ║      ║                          ║
  ║  - cosign signature     ║      ║                          ║
  ╚═════════════════════════╝      ╚══════════════════════════╝

  Later (PR 5): CD workflow → cosign verify-attestation → deploy
```
