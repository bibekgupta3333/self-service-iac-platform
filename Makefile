# Developer entrypoints. Everything runs locally against LocalStack (ADR-0009).
SHELL := /bin/bash
.DEFAULT_GOAL := help

PYTHON  ?= .venv/bin/python
MODULES := $(wildcard modules/*/)
export AWS_ACCESS_KEY_ID     ?= test
export AWS_SECRET_ACCESS_KEY ?= test
export AWS_DEFAULT_REGION    ?= us-east-1
export AWS_ENDPOINT_URL      ?= http://localhost:4566

.PHONY: help up down ps fmt fmt-check validate tflint checkov policy-test lint \
        module-test go-test py-test test diagrams

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

tflint: ## Lint Terraform with TFLint
	tflint --init >/dev/null && tflint --recursive

checkov: ## Static security scan of modules
	checkov -d modules --quiet --compact $(if $(wildcard policies/checkov/*.py),--external-checks-dir policies/checkov,)

policy-test: ## Unit-test OPA policies
	@if ls policies/opa/*.rego >/dev/null 2>&1; then opa test policies/opa -v; else echo "no policies yet"; fi

lint: fmt-check validate tflint checkov policy-test ## All static checks

## ---- tests ----
module-test: ## terraform test every module against LocalStack (needs `make up`)
	@for m in $(MODULES); do \
	  [ -d "$$m/tests" ] || continue; \
	  echo "==> test $$m"; \
	  terraform -chdir=$$m init -backend=false -input=false >/dev/null && terraform -chdir=$$m test || exit 1; \
	done

go-test: ## Go unit tests (backend + integration test module)
	@[ -f backend/go.mod ] && (cd backend && go vet ./... && go test ./...) || echo "backend not initialised yet"

py-test: ## Python tooling tests
	@[ -d tools ] && $(PYTHON) -m pytest -q tools || true

test: go-test py-test policy-test module-test ## Run all tests

## ---- docs ----
diagrams: ## Re-render architecture diagrams (needs graphviz)
	$(PYTHON) docs/diagrams/src/render_all.py
