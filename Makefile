# Developer entrypoints. Everything runs locally against LocalStack (ADR-0009).
SHELL := /bin/bash
.DEFAULT_GOAL := help

PYTHON  ?= .venv/bin/python
MODULES := $(dir $(wildcard modules/*/main.tf))

# Local-first (ADR-0009): always talk to LocalStack with dummy credentials.
# Hard-set so real credentials or profiles in the caller's shell can never leak in.
override AWS_ACCESS_KEY_ID     := test
override AWS_SECRET_ACCESS_KEY := test
override AWS_DEFAULT_REGION    := us-east-1
override AWS_ENDPOINT_URL      := http://localhost:4566
export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION AWS_ENDPOINT_URL
unexport AWS_PROFILE AWS_SESSION_TOKEN AWS_SECURITY_TOKEN

.PHONY: help up down ps fmt fmt-check validate tflint checkov policy-test lint \
        module-test module-test-floor example-plans integration-test validate-manifests module-gate codeowners codeowners-check required-checks-check go-test go-integration generator-update api portal-install portal-dev portal-test py-test test diagrams \
        stack-detect stack-plan stack-apply stack-destroy stack-clean

help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## ---- local stack ----
up: ## Start LocalStack and PostgreSQL
	docker compose up -d --wait

down: ## Stop the local stack
	docker compose down

ps: ## Show local stack status
	docker compose ps

## ---- terraform ----
fmt: ## Format all Terraform
	terraform fmt -recursive

fmt-check: ## Check Terraform formatting
	terraform fmt -recursive -check

validate: ## terraform validate every module
	@for m in $(MODULES); do \
	  [ -f "$$m/main.tf" ] || continue; \
	  echo "==> validate $$m"; \
	  terraform -chdir=$$m init -backend=false -input=false >/dev/null && terraform -chdir=$$m validate || exit 1; \
	done

TFLINT_CONFIG := $(CURDIR)/.tflint.hcl

tflint: ## Lint Terraform with TFLint (absolute config path: --recursive ignores a relative one)
	tflint --init --config "$(TFLINT_CONFIG)" >/dev/null && tflint --recursive --config "$(TFLINT_CONFIG)"

checkov: ## Static security scan of modules
	checkov -d modules --framework terraform --quiet --compact $(if $(wildcard policies/checkov/*.py),--external-checks-dir policies/checkov,)

policy-test: ## Unit-test OPA policies and verify conftest fixtures
	opa test policies/opa --coverage --threshold 90 >/dev/null
	opa test policies/opa
	policies/fixtures/verify.sh
	policies/fixtures/verify-example-gate.sh

lint: fmt-check validate validate-manifests module-gate codeowners-check required-checks-check tflint checkov policy-test ## All static checks

## ---- stacks (environments/, mirrors pr-validate.yml) ----
STACK ?= environments/development/platform/smoke
BASE  ?= origin/main
# Same switch as the workflow's IACP_PIN_BOOTSTRAP: off, so unpublished module tags are refused.
# IACP_PIN_BOOTSTRAP=true serves them from a local mirror (the pre-release ADR-0009 bootstrap).
IACP_PIN_BOOTSTRAP ?= false
PIN_BOOTSTRAP_FLAG := $(if $(filter true,$(IACP_PIN_BOOTSTRAP)),--allow-unpublished,)

stack-detect: ## Stacks changed since BASE (default origin/main) as a JSON matrix
	$(PYTHON) -m tools.stack_pipeline detect --base $(BASE) --head HEAD --github-output ''

stack-plan: ## pr-validate checks + plan + conftest for STACK against LocalStack (needs `make up`)
	@set -euo pipefail; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	exports="$$($(PYTHON) -m tools.stack_pipeline verify-pins $(STACK) --mirror "$$tmp/mirror.git" $(PIN_BOOTSTRAP_FLAG) --github-env '')"; \
	eval "$$exports"; \
	terraform -chdir=$(STACK) fmt -check -recursive; \
	terraform -chdir=$(STACK) init -input=false -lockfile=readonly >/dev/null; \
	$(PYTHON) -m tools.stack_pipeline verify-modules $(STACK); \
	terraform -chdir=$(STACK) validate -no-color; \
	terraform -chdir=$(STACK) plan -input=false -lock=false -out=tfplan; \
	terraform -chdir=$(STACK) show -json tfplan > "$$tmp/plan.json"; \
	$(PYTHON) -m tools.stack_pipeline context $(STACK) > "$$tmp/context.json"; \
	conftest test "$$tmp/plan.json" -p policies/opa --data "$$tmp/context.json"

stack-apply: ## Apply the saved STACK plan to LocalStack (run stack-plan first)
	terraform -chdir=$(STACK) apply -input=false tfplan

stack-destroy: ## Destroy STACK on LocalStack
	terraform -chdir=$(STACK) destroy -input=false -auto-approve

stack-clean: ## Remove local state, plan and .terraform from STACK
	@test -f "$(STACK)/iacp.stack.json" || { echo "STACK=$(STACK) is not a stack root"; exit 1; }
	rm -rf $(STACK)/.terraform $(STACK)/tfplan $(STACK)/terraform.tfstate $(STACK)/terraform.tfstate.backup

## ---- tests ----
example-plans: ## Plan every examples/complete and run conftest with its policy-context.json (needs `make up`)
	@for ctx in modules/*/examples/complete/policy-context.json; do \
	  policies/example-plan.sh "$${ctx%/examples/complete/policy-context.json}" || exit 1; \
	done

module-test: ## terraform test every module (LocalStack suites need `make up`; mock_provider suites run offline)
	@for m in $(MODULES); do \
	  [ -d "$$m/tests" ] || continue; \
	  echo "==> test $$m"; \
	  terraform -chdir=$$m init -backend=false -input=false >/dev/null && terraform -chdir=$$m test || exit 1; \
	done

module-test-floor: ## Offline terraform test suites on the minimum provider versions (mirrors the module-tests-floor CI job)
	@export TF_PLUGIN_CACHE_DIR=$${TF_PLUGIN_CACHE_DIR:-$$HOME/.terraform.d/plugin-cache} TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE=true; \
	mkdir -p "$$TF_PLUGIN_CACHE_DIR"; stage=$$(mktemp -d); trap 'rm -rf "$$stage"' EXIT; \
	for m in $(MODULES); do \
	  name=$$(basename $$m); \
	  grep -q '^mock_provider "aws"' $$m/tests/*.tftest.hcl 2>/dev/null || continue; \
	  echo "==> test $$name on its provider floors"; \
	  $(PYTHON) -m tools.provider_floor stage $$m "$$stage/$$name" \
	    && terraform -chdir="$$stage/$$name" init -backend=false -input=false -upgrade >/dev/null \
	    && terraform -chdir="$$stage/$$name" test || exit 1; \
	done

go-test: ## Go unit tests (backend)
	@if [ -f backend/go.mod ]; then \
	  cd backend && go vet ./... && go test ./...; \
	else \
	  echo "backend not initialised yet"; \
	fi

go-integration: ## Go integration tests against local Postgres (needs `make up`)
	cd backend && IACP_INTEGRATION=1 go test -race -count=1 ./...

generator-update: ## Regenerate generator golden files after an intended template change
	cd backend && go test ./internal/generator -update

api: ## Run the API locally in dev auth mode (needs `make up`)
	cd backend && IACP_AUTH_MODE=dev IACP_INTERNAL_TOKEN=local-dev-internal-token-0123456789abcdef go run ./cmd/api

## ---- portal ----
PORTAL_API_URL ?= http://127.0.0.1:8080

portal-install: ## Install portal dependencies from the lock file (Node version in portal/.nvmrc)
	cd portal && npm ci --no-audit --no-fund

portal-dev: ## Run the portal on :3000 against the local API (needs `make api`)
	@[ -d portal/node_modules ] || $(MAKE) portal-install
	cd portal && IACP_API_URL=$(PORTAL_API_URL) npm run dev

portal-test: ## Portal lint, typecheck and unit tests
	@[ -d portal/node_modules ] || $(MAKE) portal-install
	cd portal && npm run lint && npm run typecheck && npm test

integration-test: ## Terratest suite in tests/ against LocalStack (needs `make up`)
	cd tests && go test -count=1 -timeout 45m ./...

validate-manifests: ## Validate every modules/*/module.yaml against the schema and variables.tf
	$(PYTHON) -m tools.manifest_validator modules/

codeowners: ## Regenerate .github/CODEOWNERS from .github/teams.yaml
	$(PYTHON) -m tools.codeowners

required-checks-check: ## Fail when .github/rulesets/main-branch.json requires a check no workflow job is named
	$(PYTHON) -m tools.required_checks --check

codeowners-check: ## Fail when .github/CODEOWNERS drifted from .github/teams.yaml
	$(PYTHON) -m tools.codeowners --check

module-gate: ## SemVer interface-diff gate: every module vs its last modules/<name>/vX.Y.Z tag
	$(PYTHON) -m tools.module_release gate --all

py-test: ## Python tooling tests (exit code 5 = no tests collected is OK)
	@$(PYTHON) -m pytest -q tools policies; rc=$$?; \
	if [ $$rc -eq 5 ]; then echo "no python tests yet"; exit 0; fi; exit $$rc

test: go-test py-test policy-test module-test ## Run all tests

## ---- docs ----
diagrams: ## Re-render architecture diagrams (needs graphviz)
	$(PYTHON) docs/diagrams/src/render_all.py
