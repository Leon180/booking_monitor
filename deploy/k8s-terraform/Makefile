# Operator targets for the k8s-terraform stack. Mirrors PR 1's
# deploy/terraform/Makefile pattern.

PROJECT_ID ?= booking-monitor-sandbox
STATE_BUCKET = $(PROJECT_ID)-tfstate
STATE_PREFIX = booking-monitor/k8s-terraform

.PHONY: help init plan apply destroy verify fmt validate

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

init: ## terraform init against the GCS backend
	terraform init -backend-config="bucket=$(STATE_BUCKET)"

plan: ## terraform plan (eyeball before apply)
	terraform plan -out=tfplan

apply: ## terraform apply (interactive — review the diff first!)
	@printf '\033[31mWARNING:\033[0m This creates real GCP resources that COST MONEY.\n'
	@printf 'Expected cost: ~$$30-35/month (GKE Autopilot pod + Cloud SQL).\n'
	@printf 'Set a Cloud Billing budget alert BEFORE running apply.\n\n'
	terraform apply tfplan

destroy: ## terraform destroy (tears everything down — irreversible)
	@printf '\033[31mWARNING:\033[0m This deletes the GKE cluster + Cloud SQL data.\n'
	@printf 'Backup Cloud SQL first if you care about the data.\n\n'
	terraform destroy

verify: fmt validate ## Run fmt + validate locally

fmt: ## terraform fmt -recursive
	terraform fmt -recursive -diff

validate: ## terraform validate (no backend; requires init first)
	terraform validate -no-color
