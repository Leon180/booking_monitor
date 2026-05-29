---
name: iac-verify
description: Use BEFORE asserting cloud API behavior, IAM binding semantics, or Terraform schema details. Triggers on any *.tf change, cloud-init script edit, WIF / IAM binding work, or supply-chain (cosign / SLSA) configuration. Forces a verification pass against official provider docs + live cloud state instead of relying on training memory.
---

# IaC Verification Protocol

**Why this skill exists**: This project's `infra/gcp-bootstrap` PR shipped a CRIT bug where `principalSet://...attribute.ref/refs/tags/v` was treated as a prefix match (training memory) when GCP IAM treats it as exact equality (actual docs). Three independent reviewers caught it; it should have been caught at write-time. This skill is the forcing function so that class of error doesn't ship again.

## Trigger conditions

Activate when ANY of the following is true:

- Editing `*.tf`, `*.tf.json`, `*.tfvars`, or `.terraform.lock.hcl`
- Editing `cloud-init/*.sh` or any VM bootstrap script
- Adding or modifying IAM bindings (`*.iam.gserviceaccount.com`, `principal://`, `principalSet://`, AWS role trust policies, Azure RBAC)
- Writing Workload Identity Federation, OIDC trust, or SLSA / cosign configuration
- Asserting cloud API semantics in chat (e.g. "this attribute does X", "GCP supports Y")
- Generating cloud CLI commands (`gcloud`, `aws`, `az`, `kubectl`) in code or runbooks

## Mandatory verification steps

Before commit OR before claiming "X is the right config", complete this checklist:

### 1. Provider schema check (Terraform MCP)

Use the `terraform` MCP server's `resolveProviderDocID` + `getProviderDocs` tools (or equivalent) to:

- Confirm every resource type used exists in the pinned provider major version
- Confirm every attribute name + nesting structure matches the resource schema
- Confirm every IAM URI scheme used (`principal://` vs `principalSet://` vs `serviceAccount:`) — read the actual `members` field docs
- Read the Examples section if available; if your config diverges from examples, justify why in a comment

### 2. Live cloud state check (gcloud MCP)

When the work touches an existing project:

- Use the `gcloud` MCP to query active state (`gcloud iam workload-identity-pools providers describe`, `gcloud projects get-iam-policy`, etc.) and compare against what the .tf file produces
- For new resources: simulate via `terraform plan` against a sandbox and inspect the diff before applying

### 3. CEL expressions (WIF specific)

GCP `attribute_condition` + `attribute_mapping` are CEL expressions, NOT Terraform interpolation:

- String literals use `"..."` not single quotes
- `==`, `&&`, `||` operators
- `.startsWith(...)`, `.endsWith(...)`, `.contains(...)` methods on strings
- The full claim namespace is under `assertion.X` (e.g. `assertion.repository_owner`, `assertion.ref`, `assertion.workflow_ref`)

Verify CEL syntax against the [CEL spec](https://github.com/google/cel-spec/blob/master/doc/langdef.md) BEFORE relying on it.

### 4. IAM principal URI semantics

The three URI schemes that look interchangeable but are NOT:

| Scheme | Match rule | Use case |
| --- | --- | --- |
| `principal://...subject/<exact>` | Exact match on the `sub` claim | Pinning a single deploy identity (e.g. `main` branch) |
| `principalSet://...attribute.X/<value>` | **Exact equality** on attribute `X`'s mapped value | Group of identities that share an attribute value (NOT prefix) |
| `serviceAccount:<email>` | Service account identity | Granting roles to an SA |

**Common Q3-class mistake**: assuming `principalSet://...attribute.ref/refs/tags/v` matches any `v*` tag. It does NOT. Use `attribute_mapping` to derive a CEL-computed attribute (e.g. `assertion.ref.startsWith("refs/tags/v") ? "v" : ""`) and bind on the derived attribute.

## Verification commands

Quick checks before commit:

```bash
# Validate Terraform syntax
terraform fmt -check -recursive
terraform validate

# Show actual provider docs for a resource
# (run via Terraform MCP from Claude Code; example invocation:)
# resolveProviderDocID provider=google resource=google_iam_workload_identity_pool_provider

# Verify WIF binding after apply
gcloud iam service-accounts get-iam-policy <SA_EMAIL> --format=json | jq

# Verify Docker apt pin took effect (Debian VM hosts)
apt-cache policy docker-ce | head -10

# Verify unattended-upgrades pattern matches actual archive
unattended-upgrades --dry-run -d 2>&1 | grep -E "Allowed origins|Pkgs that look|Adjusting"
```

## Anti-patterns to refuse

- Writing a `principalSet://...attribute.X/<value>` binding without verifying `<value>` is the EXACT attribute value (no implicit prefix)
- Asserting a Terraform attribute exists without checking the provider schema for the pinned major version
- Pinning an apt package version with a glob like `5:28.*` without verifying the pin syntax via `apt-cache policy <package>` post-apply
- Adding `depends_on = [api]` and assuming API propagation is synchronous — GCP APIs have ~30-60s eventual consistency; add explicit `time_sleep` resources OR document the retry pattern
- Quoting an authoritative claim (e.g. "GitHub OIDC `sub` claim format is X") without a primary-source URL

## Output discipline

Every cloud-API assertion in chat or commit messages must be either:

- (a) Backed by a Terraform MCP / gcloud MCP query result (cite which tool + query), OR
- (b) Backed by a primary-source URL (cloud.google.com, registry.terraform.io, docs.github.com), OR
- (c) Explicitly marked UNVERIFIED so reviewers know to challenge it

If neither (a) nor (b) is available, do not assert. Ask the user or open the question explicitly.

## When to invoke other skills

- `verification-before-completion` — before committing or opening PR
- `project-review-checkpoint` — at phase boundaries (PR 1-8 milestones)
- `karpathy-guidelines` — when tempted to add abstractions / complexity beyond what's needed

## Origin

Created 2026-05-29 in response to PR 1 CRIT (`workload_identity.tf:99` principalSet semantics misread). See `docs/reviews/` history if maintained, or the chat transcript that introduced this skill.
