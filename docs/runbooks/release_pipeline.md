# Release pipeline runbook

End-to-end walkthrough of the supply-chain-hardened publish pipeline introduced in PR 3. By the time you finish this runbook, every image pushed to Artifact Registry will be:

- Scanned for HIGH/CRITICAL CVEs (Trivy gate; publish fails if any are found)
- Attested with SLSA L3 build provenance (via `actions/attest-build-provenance`)
- Bundled with a SPDX SBOM (via Syft + cosign attest keyless)
- Signed by ephemeral keys with a cert SAN bound to the exact workflow that built it

## One-time setup

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

If both attestations verify, the release pipeline is wired correctly. Delete the test tag (it's still tracked in AR — cleanup with `gcloud artifacts docker tags delete ...`):

```bash
git tag -d v0.0.0-rc1
git push origin :refs/tags/v0.0.0-rc1
```

## Day-to-day usage

Every push to `main` automatically:
- Builds an image tagged `:main` + `:sha-<7-char-sha>`
- Trivy-scans
- Publishes to Artifact Registry
- Signs + attests provenance + SBOM

Every push of a `v*.*.*` tag adds `:<version>` + `:latest` tags (in addition to the sha tag).

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

- Image signing identity verification at deploy time → PR 5 (CD workflow `cosign verify` gate)
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
