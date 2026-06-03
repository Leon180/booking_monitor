# k8s migration runbook — VM → GKE Autopilot

Operator procedure to apply [`deploy/k8s-terraform/`](../../deploy/k8s-terraform/) + roll out the application onto GKE. Companion to [`docs/k8s-migration-plan.md`](../k8s-migration-plan.md) (which is the design document).

## ⚠️ Cost guard rails (DO THESE FIRST)

Before applying any Terraform here:

1. **Set a Cloud Billing budget alert** at $50/month:
   ```bash
   gcloud beta billing budgets create \
     --billing-account=$(gcloud beta billing accounts list --format='value(name)' | head -1) \
     --display-name="booking-monitor sandbox" \
     --budget-amount=50USD \
     --threshold-rule=percent=0.5 \
     --threshold-rule=percent=0.9 \
     --threshold-rule=percent=1.0 \
     --calendar-period=MONTH \
     --filter-projects=projects/<your-project-number>
   ```
2. **Decide your "active demo period"** — only run the cluster while you're actively job-hunting / demoing. `terraform destroy` between active periods.

Expected monthly cost per [`docs/k8s-migration-plan.md`](../k8s-migration-plan.md) cost table:
- Cluster fee: **$0** (free credit covers first cluster)
- App pod: ~$20
- Cloud SQL db-f1-micro + 10GB SSD: ~$9–12
- Cloudflare Tunnel: $0
- **Total: ~$30–35/month** while running

## Sequence (do NOT skip steps)

The migration is sequential — VM keeps serving traffic until the cluster is fully validated, then we cut Cloudflare DNS over in one operation.

### Step 1 — Apply the k8s Terraform

```bash
cd deploy/k8s-terraform
cp terraform.tfvars.example terraform.tfvars
vi terraform.tfvars # fill in project_id (must match PR 1's terraform)

make verify  # fmt + validate
make init    # backend init against PR 1's GCS state bucket
make plan    # eyeball: ~5 resources (cluster, sql instance, db, user, IAM binding)
make apply   # 5-10 min — GKE Autopilot creation is slow
```

Capture outputs:

```bash
terraform output -json > /tmp/k8s-outputs.json
CLUSTER_NAME=$(jq -r .cluster_name.value /tmp/k8s-outputs.json)
CLUSTER_LOC=$(jq -r .cluster_location.value /tmp/k8s-outputs.json)
SQL_CONN=$(jq -r .cloudsql_connection_name.value /tmp/k8s-outputs.json)
```

### Step 2 — Set Cloud SQL DB password + populate DATABASE_URL secret

Cloud SQL passwords don't go through Terraform state. Set after apply:

```bash
DB_PWD=$(openssl rand -hex 32)
gcloud sql users set-password booking-app \
  --instance=booking-monitor-db \
  --password="$DB_PWD"

# Construct the DATABASE_URL for the Cloud SQL Auth Proxy sidecar (127.0.0.1:5432)
URL="postgres://booking-app:${DB_PWD}@127.0.0.1:5432/booking?sslmode=disable"
printf "%s" "$URL" | gcloud secrets versions add database-url --data-file=-
```

### Step 3 — Get kubeconfig for the new cluster

```bash
gcloud container clusters get-credentials "$CLUSTER_NAME" \
  --region="$CLUSTER_LOC" \
  --project=<your-project-id>

# Verify access:
kubectl cluster-info
kubectl get nodes  # Autopilot may report fewer nodes than expected — that's normal
```

### Step 4 — Install the Secrets Store CSI Driver + Google's secret-manager-managed-csi add-on

GKE Autopilot includes the CSI Driver but the Secret Manager provider needs to be installed:

```bash
# Enable the add-on on the existing cluster (one-time):
gcloud container clusters update "$CLUSTER_NAME" \
  --region="$CLUSTER_LOC" \
  --update-addons=SecretManagerSecretProviderClass=ENABLED

# Verify:
kubectl get crd secretproviderclasses.secrets-store.csi.x-k8s.io
```

### Step 5 — Apply k8s manifests

The manifests in [`deploy/k8s/manifests/`](../../deploy/k8s/manifests/) have placeholders that need substitution:

```bash
cd deploy/k8s/manifests
PROJECT_ID=<your-project-id>
REGION=us-central1

# Substitute placeholders (raw sed for portfolio; would use kustomize for prod):
for f in *.yaml; do
  sed -i.bak "s|<PROJECT_ID>|${PROJECT_ID}|g; s|<REGION>|${REGION}|g" "$f"
done
rm *.yaml.bak

# Apply in dependency order:
kubectl apply -f namespace.yaml
kubectl apply -f serviceaccount.yaml
kubectl apply -f secrets-csi.yaml
kubectl apply -f service.yaml
kubectl apply -f deployment.yaml
kubectl apply -f podmonitoring.yaml
# cloudflared deployment requires a tunnel-token secret first — see Step 6
```

Verify pod comes up:

```bash
kubectl get pods -n booking-monitor -w
# Wait for booking-monitor-* to be Running + Ready
```

### Step 6 — Provision Cloudflare Tunnel token + apply cloudflared

The cloudflared deployment needs a tunnel token from Cloudflare:

```bash
# In the Cloudflare Zero Trust dashboard:
#   1. Networks → Tunnels → Create tunnel
#   2. Name: booking-monitor-k8s
#   3. Save the install command — extract the token (--token <TOKEN_VALUE>)
#   4. Set up Public Hostname → booking.<your-domain>.com → Service:
#      HTTP → booking-monitor.booking-monitor.svc.cluster.local:8080

# Store the token as a k8s Secret:
kubectl create secret generic cloudflared-tunnel-token \
  --namespace=booking-monitor \
  --from-literal=token='<TOKEN_VALUE>'

# Apply the cloudflared Deployment:
kubectl apply -f deploy/k8s/manifests/cloudflared-deployment.yaml
```

### Step 7 — Cut DNS over

Right now the VM still serves `booking.<your-domain>.com`. In the Cloudflare Zero Trust dashboard:

1. The NEW tunnel from Step 6 has a Public Hostname route mapped to k8s
2. The OLD tunnel (on VM) ALSO has a route for the same hostname
3. Disable the OLD tunnel's route (or delete the OLD tunnel entirely)
4. Cloudflare DNS now resolves to the NEW (k8s) tunnel only

Smoke test:

```bash
curl -fsS https://booking.<your-domain>.com/readyz
curl -fsS https://booking.<your-domain>.com/livez
```

### Step 8 — Shut down VM (after successful cutover)

Once k8s is serving traffic + the smoke test passes for 24h:

```bash
gcloud compute instances stop booking-app --zone=us-central1-a
# Wait a few days to confirm no rollback needed, then:
gcloud compute instances delete booking-app --zone=us-central1-a
```

PR 1's terraform still defines the VM. Either:
- (a) Edit `deploy/terraform/compute.tf` to remove the VM, run `terraform apply` to clean up
- (b) Leave PR 1's terraform alone, just `gcloud delete` the VM out of band (drift; OK for sandbox)

## Operational tasks

### kubectl basics for booking_monitor

```bash
# Logs
kubectl logs -n booking-monitor -l app.kubernetes.io/name=booking-monitor --tail=100 -f

# Exec into a pod (debugging)
kubectl exec -n booking-monitor -it <pod-name> -- /bin/sh
# Note: distroless image has no shell. Use ephemeral debug container:
kubectl debug -n booking-monitor <pod-name> -it --image=alpine

# Scale up replicas
kubectl scale -n booking-monitor deploy/booking-monitor --replicas=2

# Manual rollout (NOT GitOps — use only in emergencies)
kubectl set image -n booking-monitor deploy/booking-monitor \
  app=<region>-docker.pkg.dev/<project>/booking/booking-monitor:v1.3.2
kubectl rollout status -n booking-monitor deploy/booking-monitor
```

### Cost monitoring

```bash
# What's running:
kubectl top pods -n booking-monitor       # CPU + mem actuals
kubectl get pods -n booking-monitor       # replica count

# Billing:
gcloud billing accounts list
# Open the Cloud Billing console for the project, check Reports
```

### Disaster recovery

- **Pod crash**: Autopilot reschedules automatically; check `kubectl describe pod <pod>` for events
- **Cluster down**: rare on Autopilot. `gcloud container clusters describe <cluster>` shows status
- **DB unrecoverable**: Cloud SQL backups restore via `gcloud sql backups restore <BACKUP_ID> --restore-instance=booking-monitor-db`
- **Tunnel down**: cloudflared has 2 replicas; if BOTH fail, restart via `kubectl rollout restart -n booking-monitor deploy/cloudflared`

### Destroy procedure (between job-hunt periods)

```bash
# 1. Tear down k8s workloads (optional, but cleaner)
kubectl delete namespace booking-monitor

# 2. Destroy GKE + Cloud SQL
cd deploy/k8s-terraform
make destroy

# 3. Confirm zero billable resources remaining
gcloud compute instances list  # only any non-k8s VMs
gcloud sql instances list      # empty
gcloud container clusters list # empty
```

This is the "park the cluster" path. The VM (PR 1) remains the always-on $0-fallback. Re-apply this stack when actively demoing again.

## Trust chain comparison

Today's deploy.yml flow (PRs 5-6 on VM):
```
GH Actions runner
  → WIF OIDC → sa-ci-deploy GCP token
  → gcloud ssh --tunnel-through-iap (osAdminLogin)
  → on VM: ephemeral OS Login user → sudo
  → sudo docker pull + docker compose up
```

After migration (with ArgoCD, future PR):
```
release-please tags v1.X.Y (via PR #143 GH App token) → AR push fires
ArgoCD (inside cluster)
  → polls Git for new tags
  → kubectl apply via cluster-internal RBAC
  → kubelet pulls image + starts pod
```

The k8s path has **zero operator OS-level credentials** outside the cluster. Smaller attack surface; auditable via ArgoCD's UI.

(Caveat: kubelet still has node-level concerns — OOM, scheduling, image pull failures — but those are GKE Autopilot's problem, not ours.)

## Open gaps (deferred to PR 9 / future PRs)

- **ArgoCD installation + ApplicationSet config** — not in PR 8. Operator does this AFTER cluster apply.
- **`.github/workflows/deploy.yml` swap-out** — currently still does SSH-to-VM. Needs replacement with an ArgoCD-driven path. Premature to do in PR 8 (would break VM deploys before cluster is ready).
- **DORA dashboard preservation** — ArgoCD doesn't natively create GitHub Deployment records. Need a sidecar action that posts to the GH API on each successful ArgoCD sync.
- **SLI/SLO + chaos baseline** — PR 9. Required to legitimately call this "production-grade".
- **Cloud SQL → app connection** — current deployment.yaml assumes a Cloud SQL Auth Proxy sidecar at 127.0.0.1:5432 but the sidecar isn't declared yet. Add when actually rolling out.

## References

- [GKE Autopilot setup](https://cloud.google.com/kubernetes-engine/docs/how-to/creating-an-autopilot-cluster)
- [Secrets Store CSI for GKE](https://cloud.google.com/secret-manager/docs/secret-manager-managed-csi-component)
- [Cloud SQL Auth Proxy on GKE](https://cloud.google.com/sql/docs/postgres/connect-kubernetes-engine)
- [Cloudflare Tunnel on k8s](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/get-started/deployment-guides/kubernetes/)
- [ArgoCD Getting Started](https://argo-cd.readthedocs.io/en/stable/getting_started/)
