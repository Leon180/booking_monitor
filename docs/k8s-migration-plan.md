# k8s migration plan — booking_monitor VM → GKE Autopilot

> PR 8 of the 8-PR CI/CD roadmap. **Plan + skeleton + apply procedure**, not the migration itself.
> Operator-driven apply is documented in [docs/runbooks/k8s_migration.md](runbooks/k8s_migration.md).

## Scope

This document covers the decision tree, cost analysis, and migration sequence for moving booking_monitor from its current Docker-Compose-on-e2-micro deployment (PR 1–PR 7) to GKE Autopilot. The companion artifacts:

- [`deploy/k8s-terraform/`](../deploy/k8s-terraform/) — Terraform for GKE Autopilot cluster + Cloud SQL + cloudflared Deployment (uncommitted state; not auto-applied)
- [`deploy/k8s/manifests/`](../deploy/k8s/manifests/) — Kubernetes Deployment / Service / Ingress / ConfigMap / ServiceAccount / PodMonitoring manifests
- [`docs/runbooks/k8s_migration.md`](runbooks/k8s_migration.md) — operator procedure to actually apply Terraform + roll out the application

## TL;DR

| Decision | Choice | Why |
|---|---|---|
| **Cluster type** | GKE Autopilot | Workload-only billing model + Google manages nodes. Cheapest entry to production-grade k8s. |
| **Image runtime** | containerd (Autopilot default) | Our PR 2 distroless OCI image works as-is — no rebuild needed |
| **Database** | Cloud SQL for PostgreSQL (db-f1-micro) | Managed backups + HA path; container-PG was always a dev-only smell |
| **Secrets** | Secrets Store CSI Driver | Google's official recommendation for Autopilot v1.25+; native WIF integration |
| **Network ingress** | cloudflared Deployment + Cloudflare Tunnel | Preserves PR 4 setup; free; no GCLB cost |
| **Deploy mechanism** | ArgoCD (GitOps pull-model) | 2026 consensus for GKE; replaces deploy.yml's SSH model |
| **Observability** | Managed Service for Prometheus + existing Grafana | Auto-pulls free system metrics; same `/metrics` endpoint surface |
| **Manifest tooling** | Raw YAML (single environment) | Helm 4 is current but overkill until we have dev/staging/prod splits |
| **Release model** | release-please → tag → ArgoCD ApplicationSet | Same release-please flow; just replaces deploy.yml's apply step |
| **Apply this PR's Terraform?** | Operator decision | ~$20/month all-in for a sandbox cluster; defensible for active job-hunting period |

## Cost analysis (verified against 2026 sources)

Numbers from [CloudZero GKE Pricing 2026](https://www.cloudzero.com/blog/gke-pricing/), [Finout GKE Pricing](https://www.finout.io/blog/gke-pricing-tiers), [Usage.ai Cloud SQL 2026](https://www.usage.ai/blogs/gcp/cloud-sql/pricing/), and [GCP Free Tier docs](https://cloud.google.com/free/docs/free-cloud-features).

| Component | Cost (us-central1, 2026-06) | Notes |
|---|---|---|
| GKE Autopilot cluster fee | **$0/month** for first cluster | $73/month list price, fully covered by Google's **$74.40/month free credit** (one zonal/Autopilot cluster per billing account) |
| Pod: 0.5 vCPU + 512Mi RAM | **~$19.65/month** | (0.5 × $0.0445 + 0.5 × $0.0049) × 730 hours |
| Cloud SQL db-f1-micro | **~$7-10/month** | Region-dependent; us-central1 ~$7.67. Shared-core tier still exists in 2026 despite earlier rumors of removal. |
| Cloud SQL 10GB SSD | **~$2.20/month** | $0.22/GB/month |
| Cloud SQL backup retention (7-day default) | **$0/month** | Automated backups included free |
| Cloudflare Tunnel | **$0/month** | 1,000 tunnels/account, no usage limits in 2026 |
| Egress (low traffic) | **~$1-3/month** | Outbound to Cloudflare edge; rough estimate for portfolio-traffic-volumes |
| **Total** | **~$30-35/month** | Most ($20) is the application pod itself |

### Why this is NOT $91/month

My earlier estimate of $91/month ignored Google's **$74.40/month free credit for GKE cluster management**. With one cluster per account, the cluster fee is effectively zero. The remaining cost is pod-resource pricing — which scales with how much CPU/RAM we request, not how big the cluster is.

For portfolio scope (active job-hunting period, 3-6 months), ~$30-35/month total is defensible.

### Cost guard rails

- **Hard-stop budget alert** at $50/month via Cloud Billing → Budgets & alerts. Mandatory for sandbox accounts.
- **Auto-shutdown of pods overnight** (CronJob + scale-to-zero) if cost becomes concerning. Defers to need.
- **Free tier exhaustion** is the risk: e2-micro free tier doesn't transfer to GKE. Once we apply Autopilot Terraform, we own the cluster bill.

## The 11-component migration framework

Original Topic 8 framing was 8 components. Fact-check found 3 missing categories that the 2026 [Google Cloud Architecture Center](https://cloud.google.com/architecture) + [LoginLine migration guide](https://www.loginline.com/en/blog/migration-kubernetes-guide-2026) include:

| # | Component | VM today (PR 1-7) | GKE target | This PR's status |
|---|---|---|---|---|
| 1 | **Compute** | Docker Compose on e2-micro | GKE Autopilot pool | Terraform skeleton in [`deploy/k8s-terraform/`](../deploy/k8s-terraform/) |
| 2 | **Image runtime** | Docker daemon | containerd (Autopilot default) | No change — PR 2 distroless OCI image works as-is |
| 3 | **Secrets** | Secret Manager → `.env` file (PR 4) | Secrets Store CSI Driver | Documented in manifests below |
| 4 | **Network ingress** | nginx + cloudflared systemd unit | cloudflared as k8s Deployment | Manifest: `deploy/k8s/manifests/cloudflared-deployment.yaml` |
| 5 | **Database** | postgres container (compose) | Cloud SQL (managed) | Terraform creates Cloud SQL instance |
| 6 | **Deploy mechanism** | `docker compose up` via SSH (PR 4-5) | ArgoCD GitOps OR Cloud Deploy | Recommendation only; impl in a future PR |
| 7 | **Observability** | Prometheus + Grafana on VM | Managed Service for Prometheus + same Grafana | PodMonitoring CRD manifest example |
| 8 | **Release model** | release-please → tag → deploy.yml SSH (PR 5-6) | release-please → tag → ArgoCD ApplicationSet | Same release-please; replace deploy.yml only |
| 9 | **GitOps + RBAC + Admission Control** | n/a (VM has no equivalent) | ArgoCD + k8s RBAC + Gatekeeper (optional) | Documented; impl deferred |
| 10 | **Stateful workload strategy** | docker-compose with volume mount | Cloud SQL (managed) + StatefulSet for any future stateful pods | Cloud SQL is the answer; no in-cluster state |
| 11 | **Cost visibility** | None (Always-Free e2-micro = $0) | GCP Billing API + budget alerts | Setup checklist in runbook |

## Decision: ArgoCD vs Cloud Deploy vs raw kubectl

Three viable patterns. 2026 industry data:

| Tool | When it's the right call | Trade-off |
|---|---|---|
| **ArgoCD** | Multi-cloud, multi-environment (eventually), GitOps strict consistency. **Most GKE teams use this in 2026.** | Operational overhead — you run + monitor ArgoCD itself |
| **Cloud Deploy** | GCP-only, want progressive delivery (canary/blue-green) without operating ArgoCD | First pipeline free; additional cost. GCP-locked. |
| **raw kubectl apply** | Portfolio / single environment / "show I understand the underlying API" | No rollout history beyond `kubectl rollout undo`; no audit timeline. Industry-deprecated for prod use. |

**For booking_monitor**: ArgoCD. Reasoning:
1. We've already built release-please tagging (PR 6). ArgoCD's `ApplicationSet` reads the tag from main → auto-syncs to cluster. Clean fit.
2. The GitOps pull model means cluster credentials never leave the cluster — security improvement over the deploy SA token pattern.
3. Single environment NOW, but the migration story is "moving to k8s for prod-grade ops" → multi-env eventually. ArgoCD's progressive delivery primitives are then in place.
4. Sources: [Cloud Build to GKE via ArgoCD](https://medium.com/google-cloud/continuous-delivery-for-gke-with-cloud-deploy-and-argocd-0c3b515087ae), [GKE Fleets + ArgoCD](https://cloud.google.com/blog/products/containers-kubernetes/empower-your-teams-with-self-service-kubernetes-using-gke-fleets-and-argo-cd).

## Helm 4 (May 2026) note

Helm 3 EOL November 2026. If/when we adopt Helm (e.g., once we have dev/staging/prod), pin to Helm 4.2+ from the start. [Helm releases](https://github.com/helm/helm/releases).

For this PR's scope (single-env raw YAML), no Helm dependency.

## Manifests overview

Files in [`deploy/k8s/manifests/`](../deploy/k8s/manifests/):

| File | Purpose |
|---|---|
| `namespace.yaml` | Dedicated `booking-monitor` namespace |
| `serviceaccount.yaml` | App's k8s ServiceAccount, bound to GCP `sa-app-runtime` via Workload Identity |
| `deployment.yaml` | The Go app pod spec (1 replica, 0.5 vCPU + 512Mi RAM) |
| `service.yaml` | ClusterIP for cloudflared to target |
| `secrets-csi.yaml` | SecretProviderClass pulling from Secret Manager via CSI Driver |
| `cloudflared-deployment.yaml` | cloudflared as Deployment (replaces VM's systemd unit) |
| `podmonitoring.yaml` | Managed Prometheus CRD — auto-scrape `/metrics` |

These are **starter templates**. The actual rollout requires Cloud SQL connection strings, Cloudflare Tunnel ID, Workload Identity binding — operator fills in via runbook procedure.

## Why we still call this "skeleton" not "complete migration"

PR 8 commits the artifacts; PR 8 does NOT:
- `terraform apply` the GKE cluster (operator step; costs money)
- Deploy the application manifests to a cluster (operator step; depends on apply)
- Replace `.github/workflows/deploy.yml` with k8s-targeting equivalent (would break VM deploys if done before cluster is live)
- Migrate Postgres data from compose-PG to Cloud SQL (separate operator step)
- Reconfigure Cloudflare Tunnel from VM to cluster (DNS-coordinated step)

These are sequential operator actions. The runbook ([docs/runbooks/k8s_migration.md](runbooks/k8s_migration.md)) walks through them. Order of operations matters — premature merge of "deploy.yml → kubectl apply" would break the existing VM deploys before the cluster is ready.

## Trust chain comparison: VM (SSH) vs k8s (ArgoCD)

Today's deploy path:
```
GH Actions runner
  → WIF OIDC → sa-ci-deploy
  → gcloud ssh --tunnel-through-iap (osAdminLogin)
  → VM as ephemeral OS Login user (sudo)
  → sudo docker pull + docker compose up
  → docker daemon (root)
```
5 trust links, ending at root-equivalent on the VM.

k8s + ArgoCD path:
```
GH Actions runner
  → release-please tags v1.X.Y → push to AR
ArgoCD (running in cluster)
  → polls Git repository for new tags
  → pulls Deployment YAML referencing the new image tag
  → kubectl apply (with cluster-internal RBAC: ApplicationSet → namespace)
  → kubelet pulls image + runs pod (no operator credentials involved)
```
0 OS-level credentials needed from outside the cluster. The cluster pulls; nothing pushes. Smaller attack surface; auditable via ArgoCD's UI.

**Caveat** (per fact-check): kubelet still has node-level concerns (OOM, scheduling, image pull failures). The "no OS exposure" claim is about *operator* OS access, not about removing kernel-level concerns entirely.

## DORA dashboard continuity

PR 7's DORA dashboard reads GitHub Deployments API. The migration preserves this **only if** the new deploy mechanism continues to create GH Deployment records. ArgoCD does NOT natively create them — would need a sidecar action that posts to the GH API on each successful sync.

[`deploy/k8s/manifests/argocd-notifications.yaml`](../deploy/k8s/manifests/argocd-notifications.yaml) (TODO in a future PR) would configure ArgoCD to webhook GH on each sync success/failure, preserving the Deployment record path.

Until then, DORA metrics will go stale post-migration. Documented as a tracked gap.

## "Production-grade" claim caveat

This 8-PR roadmap delivered:
- Supply-chain hardening (PR 1-3)
- Operator + CI deploy (PR 4-5)
- Release automation (PR 6)
- DORA dashboard (PR 7)
- This k8s migration plan (PR 8)

The fact-check noted: to LEGITIMATELY claim "production-grade", we'd also need:
- **SLI/SLO definitions** + observability backing them
- **Chaos baseline** (a small set of failure-injection tests)
- **Capacity planning** (load testing against the cluster, not just the binary)

These are deferred to **PR 9** (post-merge of PR 8 + GKE apply). Without them, the honest framing is "production-LEAN" rather than "production-grade".

## References

- [GKE Pricing](https://cloud.google.com/kubernetes-engine/pricing)
- [GKE Autopilot Overview](https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-overview)
- [Cloud SQL Pricing](https://cloud.google.com/sql/pricing)
- [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/)
- [Secret Manager add-on for GKE](https://cloud.google.com/secret-manager/docs/secret-manager-managed-csi-component)
- [Helm releases](https://github.com/helm/helm/releases)
- [ArgoCD docs](https://argo-cd.readthedocs.io/)
- [Managed Service for Prometheus](https://cloud.google.com/stackdriver/docs/managed-prometheus)
- [Cloud Run pricing](https://cloud.google.com/run/pricing) (Cloud Run alternative for stateless workloads)
- [Cloudflare Tunnel kubernetes deployment](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/get-started/deployment-guides/kubernetes/)
- [Cloud Deploy + ArgoCD hybrid](https://medium.com/google-cloud/continuous-delivery-for-gke-with-cloud-deploy-and-argocd-0c3b515087ae)
