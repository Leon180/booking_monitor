# Terraform workflow targets. Run from this directory.
# Usage:
#   make help            list all targets
#   make verify          pre-commit verification (fmt + init -backend=false + validate)
#   make init            init with real GCS backend (requires PROJECT_ID + bucket)
#   make plan            terraform plan against the current tfvars
#   make apply           terraform apply (asks for confirmation)
#
# The `verify` target is the one codified by .claude/skills/iac-verify
# as MANDATORY before committing any .tf change. Running it is cheap
# (~10s) and catches graph cycles, schema errors, and fmt drift that
# Terraform MCP schema queries alone cannot.

.DEFAULT_GOAL := help

# ---------- Config (overridable) ----------
TFVARS      ?= terraform.tfvars
PROJECT_ID  ?= $(shell grep -E '^project_id' $(TFVARS) 2>/dev/null | head -1 | cut -d'"' -f2)
BACKEND_CFG ?= -backend-config="bucket=$(PROJECT_ID)-tfstate"

# ---------- Targets ----------
.PHONY: help verify init init-validate fmt validate plan apply destroy clean tfvars-check

help: ## Show this help.
	@printf "Terraform workflow targets:\n\n"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nConfig (override via env or `make TARGET VAR=value`):\n"
	@printf "  TFVARS=%s\n" "$(TFVARS)"
	@printf "  PROJECT_ID=%s\n" "$(PROJECT_ID)"

# ----- Pre-commit verification (mandatory per iac-verify skill) -----

verify: fmt init-validate validate ## Full pre-commit verification. ALWAYS run before committing .tf changes.
	@printf "\n\033[32m✓ Verification passed. Safe to commit.\033[0m\n"

fmt: ## terraform fmt -recursive (writes changes).
	terraform fmt -recursive -diff

init-validate: ## terraform init -backend=false (no GCS bucket needed; for validate only).
	terraform init -backend=false -upgrade=false

validate: ## terraform validate (requires init first).
	terraform validate

# ----- Real backend (requires GCS state bucket created per runbook Step 2) -----

tfvars-check: ## Sanity-check that terraform.tfvars exists and PROJECT_ID is set.
	@test -f $(TFVARS) || (printf "\033[31m✗ %s missing. Copy terraform.tfvars.example and fill in.\033[0m\n" "$(TFVARS)"; exit 1)
	@test -n "$(PROJECT_ID)" || (printf "\033[31m✗ Could not extract project_id from %s.\033[0m\n" "$(TFVARS)"; exit 1)
	@printf "\033[32m✓ tfvars OK. PROJECT_ID=%s\033[0m\n" "$(PROJECT_ID)"

init: tfvars-check ## terraform init with real GCS backend.
	terraform init $(BACKEND_CFG)

plan: tfvars-check ## terraform plan against the current tfvars.
	terraform plan -var-file=$(TFVARS)

apply: tfvars-check ## terraform apply (interactive confirmation).
	terraform apply -var-file=$(TFVARS)

# ----- Destructive ops (extra-guard) -----

destroy: tfvars-check ## terraform destroy. WARNING: secrets have prevent_destroy; see runbook tear-down.
	@printf "\033[33m⚠️  Secret containers have prevent_destroy = true.\033[0m\n"
	@printf "    Read docs/runbooks/gcp_bootstrap.md §'Tear-down' BEFORE running.\n"
	@printf "    Type 'destroy %s' to confirm: " "$(PROJECT_ID)" && read ans && [ "$$ans" = "destroy $(PROJECT_ID)" ]
	terraform destroy -var-file=$(TFVARS)

clean: ## Remove local .terraform/ cache. Does NOT touch GCS state.
	rm -rf .terraform/
	rm -f .terraform.lock.hcl
	@printf "\033[32m✓ Local cache cleaned. State bucket untouched.\033[0m\n"

# ---------- Future PRs (placeholders) ----------
# When PR 2 (Dockerfile hardening) lands, add at the project root or in
# deploy/docker/Makefile (NOT here — keep concerns separated):
#   docker-lint   hadolint Dockerfile
#   docker-build  docker build with build args
#   docker-dive   dive image-layer analysis
#
# When PR 3 (cosign / SBOM / SLSA) lands:
#   sign-local    cosign sign-blob (or sign-image after PR push)
#   sbom          syft -o spdx-json
#   provenance    slsa-github-generator local-replay
#   verify-image  cosign verify --certificate-identity-regexp ...
#
# When PR 4 (deploy mechanics) lands:
#   vm-lima       spin Lima Debian VM, exec bootstrap.sh, verify pins
#   vm-lima-down  tear down Lima VM
#   deploy-dry    deploy.sh DRY_RUN=1
