# 05 — Automation Pipeline

> Purpose: specify the GitHub Actions pipeline that turns a platform change request into validated, policy-checked, costed, approved and applied infrastructure, plus the scheduled lifecycle workflows.

Related: [01-system-architecture.md](01-system-architecture.md) · [04-module-registry.md](04-module-registry.md) · [06-lifecycle-management.md](06-lifecycle-management.md) · [07-security-and-policy.md](07-security-and-policy.md) · ADRs [0001](adr/0001-pr-based-gitops-provisioning.md), [0002](adr/0002-terraform-executes-in-github-actions.md), [0005](adr/0005-github-oidc-for-aws-access.md), [0006](adr/0006-opa-conftest-and-checkov.md), [0008](adr/0008-github-app-for-automation.md)

## Contents

1. [Overview](#1-overview)
2. [Stage reference](#2-stage-reference)
3. [Stack layout and changed-stack detection](#3-stack-layout-and-changed-stack-detection)
4. [Workflows](#4-workflows)
   - [4.1 Shared reporting action](#41-shared-reporting-action)
   - [4.2 pr-validate.yml](#42-pr-validateyml)
   - [4.3 apply.yml](#43-applyyml)
   - [4.4 drift.yml](#44-driftyml)
   - [4.5 ttl-reaper.yml](#45-ttl-reaperyml)
   - [4.6 module-release.yml](#46-module-releaseyml)
5. [Repository protection](#5-repository-protection)
6. [Caching and performance](#6-caching-and-performance)
7. [Terratest: `modules/s3` against LocalStack](#7-terratest-moduless3-against-localstack)

---

## 1. Overview

![CI/CD pipeline](diagrams/images/cicd-pipeline.png)

Principles:

- **Terraform runs only in GitHub Actions** ([ADR-0002](adr/0002-terraform-executes-in-github-actions.md)). The backend writes Git and reads results; it never holds AWS write credentials for workloads.
- **Plan in PR, apply on merge.** The PR is the approval artefact; `main` is the desired state.
- **Two roles per workload account.** `iacp-plan` (read-only + state read) for PR, drift and pre-apply checks; `iacp-apply` (scoped, boundary `iacp-boundary`) only in `apply.yml` jobs bound to a GitHub Environment.
- **What is applied is what was approved.** `apply.yml` re-plans and compares a normalized plan hash with the hash recorded at approval time.
- **Every run reports back.** Each job posts status, plan summary, cost and outputs to the Platform API using the job's GitHub OIDC token (audience `iacp-api`), so the portal and `pipeline_runs` stay in sync even if webhooks are delayed.

## 2. Stage reference

| # | Stage | Tool | Trigger | Runs as | Failure behaviour | Artifacts / outputs |
|---|---|---|---|---|---|---|
| 1 | Generate config | Platform Config Generator (Go, `backend/internal/generator`) | Request submitted in portal / API | Backend worker | Request stays `draft`, validation errors returned to user; nothing pushed | `main.tf`, `backend.tf`, `versions.tf`, `providers.tf`, `iacp.stack.json`, `.terraform.lock.hcl` |
| 2 | Create PR | GitHub App (branch, commit, PR, labels) | Generator success | Backend worker | Retries with backoff (3x); then request `failed` with `github_error` | Branch `iacp/<request-id>`, PR with labels `iacp`, `env:<env>`, `team:<team>`; request → `pr_open` |
| 3 | Format + validate | `terraform fmt -check`, `terraform validate` | `pull_request` | `iacp-plan` | Check fails; request → `failed` (generator bug: alerts platform team) | Job log |
| 4 | Lint | TFLint + AWS ruleset | `pull_request` | none (offline) | Check fails | SARIF uploaded to code scanning |
| 5 | Static security scan | Checkov (+ custom checks in `policies/checkov/`) | `pull_request` | none | Check fails on any `HIGH`/`CRITICAL` or custom `IACP_*` failure | `checkov.sarif`, summary in PR comment |
| 6 | Plan | `terraform plan -lock=false -out=tfplan` | `pull_request` | `iacp-plan` | Check fails; error surfaced in PR comment and portal | `tfplan`, `plan.json`, normalized `plan.sha256` |
| 7 | Policy | OPA via `conftest test plan.json -p policies/opa` | after plan | none | `deny` → check fails; `warn` → shown in comment | `conftest.json` |
| 8 | Cost | Infracost `breakdown` + `diff`; budget evaluated by OPA `budget.rego` | after plan | Infracost API key (secret) | Over-budget → fails unless `budget-override` approved by team admin | `infracost.json`; `cost_estimates` row |
| 9 | Report | PR comment (sticky) + `POST /v1/runs` (OIDC JWT) | end of PR job | OIDC → Platform API | Report failure does not fail check; reconciler fetches run later | `plan_summaries` row; request → `plan_ready` |
| 10 | Approve + merge | CODEOWNERS review; GitHub Environment reviewer for production | human | — | Merge blocked until all required checks + reviews | request → `approved` |
| 11 | Apply | `terraform plan` → hash check → `terraform apply tfplan` | `push` to `main` | `iacp-apply` in GitHub Environment `<env>` | Plan-hash mismatch → abort, request `failed` (`plan_mismatch`); apply error → stack `failed`, transient errors auto-retried by backend (max 2 retries with backoff) | `apply.log`, `outputs.json`; stack → `active` |
| 12 | Post-apply tests | Smoke tests; Terratest for ephemeral stacks | after apply | `iacp-plan` | Stack marked `active` with `health=degraded`, team notified; no auto-destroy | `go test` JSON report |

Statuses follow the request lifecycle `draft → pr_open → validating → plan_ready → approved → applying → applied | failed | rejected | cancelled` (see [06-lifecycle-management.md](06-lifecycle-management.md)).

## 3. Stack layout and changed-stack detection

Each stack is a directory `environments/<env>/<team>/<stack>/` generated by the platform:

```
environments/development/payments/orders-store/
├── main.tf                       # module blocks (generated)
├── backend.tf                    # S3 backend with use_lockfile = true
├── versions.tf                   # pinned terraform + provider versions
├── providers.tf                  # aws provider: region, assume_role-free (OIDC creds), default_tags
├── iacp.deps.auto.tfvars.json # upstream outputs injected by the platform (optional)
├── iacp.stack.json            # metadata used by CI (not read by Terraform)
└── .terraform.lock.hcl
```

`iacp.stack.json`:

```json
{
  "stack_id": "stk_01JD6K2W9M",
  "request_id": "req_01JD6K8T3V",
  "environment": "development",
  "team": "payments",
  "name": "orders-store",
  "account_id": "222222222222",
  "desired_state": "present",
  "ephemeral": true,
  "expires_at": "2026-12-01T00:00:00Z",
  "depends_on": ["stk_01JC9Z0F1A"],
  "terraform_version": "1.10.5"
}
```

While local-first ([ADR-0009](adr/0009-local-first-localstack-and-local-backend.md)), the generator writes a smaller `iacp.stack.json` (`backend`, `depends_on`, `environment`, `fencing_token`, `lifecycle`, `modules` with each instance's release tag and resolved commit SHA, `region`, `request_id`, `stack`, `team`; see `environments/development/platform/smoke/iacp.stack.json`). Account, ephemeral and TTL fields arrive with multi-account support and WBS 8.4.

Detection algorithm (`python -m tools.stack_pipeline detect`, used by `pr-validate.yml` and `apply.yml`):

1. `git diff --name-only <base>...<head> -- environments/` (PR: merge base vs. head; push: `github.event.before..github.event.after`).
2. Map each path to its stack root by taking the first four segments (`environments/<env>/<team>/<stack>`). **Fail closed:** any changed path under `environments/` that is not inside a valid root fails detection. That covers an unknown environment, a segment that is not a lowercase name (uppercase, spaces), and a file directly under `environments/`, an environment or a team. The only exceptions are `environments/README.md` and `environments/<env>/.gitkeep`.
3. Deduplicate and keep the roots that contain `iacp.stack.json`. A root with no tracked files left (`git ls-files`; cleanup after destroy) is skipped, because its state is already empty. Ignored local leftovers such as `.terraform/` do not count. A root that still has tracked files but no `iacp.stack.json` fails detection.
4. Emit a JSON matrix built from each `iacp.stack.json`, whose identity must match its directory: `[{ "id": "development__platform__smoke", "path": "environments/development/platform/smoke", "environment": "...", "team": "...", "stack": "...", "region": "...", "lifecycle": "active", "backend": "local" }]` (`account_id`, `desired_state` and `ephemeral` join the entry when the stack file carries them). `id` joins environment, team and stack with `__`, which names cannot contain, so ids are injective. `git diff --no-renames` reports a moved stack as a delete plus an add.
5. Guard rails: fail if more than 20 stacks changed (generator emits at most one stack per request; bulk changes require the `iacp/bulk` label and platform-team approval). Fail if two changed stacks have a `depends_on` edge between them — the backend serialises dependent stacks into separate requests (DAG ordering, see [06-lifecycle-management.md](06-lifecycle-management.md#7-dependency-management)). `depends_on` entries may name a stack as `<env>/<team>/<stack>`, by its slug id, or by its path.
6. A change to the pipeline or its inputs re-validates every stack (`--full-run-prefix`): `.github/workflows/pr-validate.yml`, `tools/stack_pipeline/`, `policies/` (OPA, custom Checkov checks and their pinned requirements), `.tflint.hcl` and `.terraform-version`. The 20-stack limit applies to change sets, not to these full runs.

Changes outside `environments/` do not plan stacks: `modules/**` triggers `module-release.yml`; `policies/**` triggers `opa test` / `conftest verify` and Checkov custom-check unit tests; `backend/**` and `portal/**` have their own app CI.

## 4. Workflows

All third-party actions are pinned to a full commit SHA, written below as `@<sha> # vX.Y.Z`; Dependabot (`package-ecosystem: github-actions`) proposes pin updates. Tool versions (Terraform, TFLint, Checkov, conftest, Infracost) are pinned in `.github/tool-versions.env`.

### 4.1 Shared reporting action

`.github/actions/iacp-report/action.yml` — a composite action that exchanges the job's OIDC token for an API call. The API validates `iss`, `aud=iacp-api`, `repository=acme/self-service-iac-platform` and `job_workflow_ref` against an allow-list of workflow files on `main` or the PR.

```yaml
name: iacp-report
description: Report pipeline status to the Platform API using GitHub OIDC
inputs:
  event:      { required: true }   # plan_ready | plan_failed | apply_started | applied | apply_failed | drift
  stack-id:   { required: true }
  payload:    { required: false, default: "{}" }   # path to JSON file or inline JSON
runs:
  using: composite
  steps:
    - shell: bash
      env:
        IACP_API_URL: ${{ vars.IACP_API_URL }}
      run: |
        set -euo pipefail
        TOKEN=$(curl -sSf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
          "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=iacp-api" | jq -r .value)
        BODY='${{ inputs.payload }}'
        [ -f "$BODY" ] && BODY=$(cat "$BODY")
        jq -n --arg event '${{ inputs.event }}' --arg stack '${{ inputs.stack-id }}' \
              --arg run "$GITHUB_RUN_ID" --arg attempt "$GITHUB_RUN_ATTEMPT" \
              --arg sha "$GITHUB_SHA" --argjson data "$BODY" \
              '{event:$event, stack_id:$stack, run_id:$run, run_attempt:$attempt, commit_sha:$sha, data:$data}' \
        | curl -sS --retry 5 --retry-all-errors --max-time 20 \
            -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
            -X POST "$IACP_API_URL/v1/runs" --data-binary @- \
          || echo "::warning::Platform API report failed; reconciler will backfill"
```

#### Current implementation (WBS 5.6)

`.github/actions/iacp-report/action.yml` differs from the sketch above. The sketch posts a payload shape the API does not accept and puts expressions inside `run:`. The action is a thin wrapper around `python -m tools.stack_pipeline report`:

- **Payload:** `RunReport` (`tools/stack_pipeline/report.py`) mirrors `runs.Report` and `runs.Validate` in `backend/internal/runs/report.go`. It sends only the declared fields, because the API decodes strictly, and a report the API would reject with `422` fails before it is sent. `plan` (hash, counts as add/change/destroy/replace, resource changes) and `policy` (`opa.deny`, `opa.warn`) come from the `summary --json-out` document. `outputs` come from `terraform output -json`, with every sensitive or unrecognised value replaced by `"<sensitive>"`. `error` is `{"class", "message"}`.
- **Provenance:** `github.head_sha` is always `GITHUB_SHA`, the OIDC token's `sha` claim. For `pull_request` events that is the merge commit, not the PR head. There is no input to override it. `run_id`, `run_attempt` and `log_url` come from the runner environment.
- **Request and token:** `request_short_id` defaults to `request_id` in `iacp.stack.json`. `fencing_token` defaults to the stack file's value only for `validate`. `apply` must pass the token from its own lock claim.
- **Auth:** in a job with `id-token: write`, a GitHub OIDC token is requested for `audience`. Such a job never falls back to the shared token: an empty audience or a failed OIDC request is an error. Only a job without `id-token: write` uses the `internal-token` input (the shared `IACP_INTERNAL_TOKEN`). The backend does not scope that token to a workflow, so no job that runs pull-request code may receive it. Today no workflow passes it. If a main-only job ever needs it, store it as an environment secret of a GitHub Environment whose deployment branches are limited to `main`, never as a repository secret. OIDC tokens are single-use: the API records each token's `jti` and answers a second use with 401, so the client mints a fresh token for every request, retries included (lock release reads the lock and then deletes it with two different tokens). Tokens travel only through `env:`, never argv, and are registered with `::add-mask::`. Messages that quote API responses are escaped, so they cannot start a workflow command.
- **Transport:** `IacpApiClient` (`tools/stack_pipeline/api_client.py`) refuses redirects, so the bearer token is never replayed to another location. It requires https except on loopback, and retries `429`/`5xx` for run reports and lock claims. A stage report replaces its earlier entry, so retries are idempotent. Lock releases are not retried.
- **Unconfigured:** with `api-url` empty (`vars.IACP_API_URL` unset, the local-first state), the step prints a `::notice::` and exits 0.
- **Failure policy:** `fail-on-error: "false"` (the default) turns a rejected or unreachable report into a `::warning::`. `apply.yml` sets it to `"true"` on the pre-apply `plan` report, so `409 stale_fencing_token` aborts the job before anything is applied.

Inputs: `api-url`, `audience`, `internal-token`, `pipeline`, `stack-path`, `stage`, `status` (a stage status or a GitHub step outcome; empty derives it from the summary), `summary-json`, `outputs-json`, `terraform-exit-code`, `error-message`, `fencing-token`, `request-short-id`, `fail-on-error`. The caller must check out the repository and set up Python first.

### 4.2 `pr-validate.yml`

The YAML below is the target design with OIDC roles and the platform API. The workflow in the repository today is the local-first version described in [Local-first implementation](#local-first-implementation-current) after the notes.

```yaml
name: pr-validate

on:
  pull_request:
    branches: [main]
    paths:
      - "environments/**"

permissions:
  contents: read

concurrency:
  group: pr-validate-${{ github.event.pull_request.number }}
  cancel-in-progress: true

env:
  TF_IN_AUTOMATION: "true"
  TF_INPUT: "false"
  TF_PLUGIN_CACHE_DIR: ${{ github.workspace }}/.terraform.d/plugin-cache
  STATE_REGION: us-east-1

jobs:
  detect:
    runs-on: ubuntu-24.04
    outputs:
      matrix: ${{ steps.detect.outputs.matrix }}
      count:  ${{ steps.detect.outputs.count }}
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
        with:
          fetch-depth: 0
      - id: detect
        run: .github/scripts/changed-stacks.sh "${{ github.event.pull_request.base.sha }}" "${{ github.event.pull_request.head.sha }}"

  validate:
    needs: detect
    if: needs.detect.outputs.count != '0'
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    permissions:
      contents: read
      id-token: write          # OIDC for AWS + Platform API
      pull-requests: write     # sticky PR comment
      security-events: write   # SARIF upload
    strategy:
      fail-fast: false
      max-parallel: 10
      matrix:
        stack: ${{ fromJSON(needs.detect.outputs.matrix) }}
    concurrency:
      group: stack-${{ matrix.stack.stack_id }}-plan-pr${{ github.event.pull_request.number }}
      cancel-in-progress: true
    defaults:
      run:
        working-directory: ${{ matrix.stack.path }}
    steps:
      - uses: actions/checkout@<sha> # v4.2.2

      - name: Load tool versions
        working-directory: .
        run: cat .github/tool-versions.env >> "$GITHUB_ENV"

      - uses: hashicorp/setup-terraform@<sha> # v3.1.2
        with:
          terraform_version: ${{ env.TERRAFORM_VERSION }}
          terraform_wrapper: false

      - name: Cache Terraform providers
        uses: actions/cache@<sha> # v4.2.0
        with:
          path: ${{ env.TF_PLUGIN_CACHE_DIR }}
          key: tf-providers-${{ runner.os }}-${{ hashFiles(format('{0}/.terraform.lock.hcl', matrix.stack.path)) }}
          restore-keys: tf-providers-${{ runner.os }}-

      - name: Report validating
        uses: ./.github/actions/iacp-report
        with: { event: validating, stack-id: "${{ matrix.stack.stack_id }}" }

      - name: terraform fmt
        run: terraform fmt -check -recursive -diff

      - name: Configure AWS credentials (plan role)
        uses: aws-actions/configure-aws-credentials@<sha> # v4.0.2
        with:
          role-to-assume: arn:aws:iam::${{ matrix.stack.account_id }}:role/iacp-plan
          role-session-name: plan-${{ github.run_id }}-${{ matrix.stack.stack_id }}
          aws-region: ${{ env.STATE_REGION }}
          role-duration-seconds: 3600

      - name: terraform init
        run: |
          mkdir -p "$TF_PLUGIN_CACHE_DIR"
          terraform init -lockfile=readonly

      - name: terraform validate
        run: terraform validate -no-color

      - name: TFLint
        run: |
          tflint --init --config "$GITHUB_WORKSPACE/modules/_shared/.tflint.hcl"
          tflint --config "$GITHUB_WORKSPACE/modules/_shared/.tflint.hcl" \
                 --call-module-type=all --format sarif > "$RUNNER_TEMP/tflint.sarif"

      - name: Checkov (static)
        run: |
          checkov -d . --download-external-modules true \
            --external-checks-dir "$GITHUB_WORKSPACE/policies/checkov" \
            --framework terraform --compact --quiet \
            -o cli -o sarif --output-file-path console,"$RUNNER_TEMP"

      - name: terraform plan
        id: plan
        run: |
          if [ "${{ matrix.stack.desired_state }}" = "absent" ]; then MODE="-destroy"; else MODE=""; fi
          terraform plan $MODE -lock=false -out=tfplan -no-color 2>&1 | tee "$RUNNER_TEMP/plan.txt"
          terraform show -json tfplan > plan.json
          # Normalized hash: ignores timestamps/run metadata, stable across re-plans of identical changes
          jq -S '[.resource_changes[] | {address, actions: .change.actions, after: .change.after}]' plan.json \
            | sha256sum | cut -d' ' -f1 > plan.sha256
          echo "hash=$(cat plan.sha256)" >> "$GITHUB_OUTPUT"

      - name: OPA / conftest
        run: |
          curl -sSf -H "Authorization: Bearer $(.github/scripts/iacp-token.sh)" \
            "${{ vars.IACP_API_URL }}/v1/internal/budget-context?stack_path=${{ matrix.stack.path }}" \
            > "$RUNNER_TEMP/policy-data.json"     # team, budget, overrides, exceptions, module_versions status, RBAC context
          conftest test plan.json \
            --policy "$GITHUB_WORKSPACE/policies/opa" \
            --data "$RUNNER_TEMP/policy-data.json" \
            --namespace iacp.plan --all-namespaces \
            --output json > conftest.json || (cat conftest.json; exit 1)

      - name: Infracost
        env:
          INFRACOST_API_KEY: ${{ secrets.INFRACOST_API_KEY }}
        run: |
          infracost breakdown --path tfplan --format json --out-file infracost.json
          conftest test infracost.json \
            --policy "$GITHUB_WORKSPACE/policies/opa/budget" \
            --data "$RUNNER_TEMP/policy-data.json" --namespace iacp.budget

      - name: Upload plan artifact
        if: always()
        uses: actions/upload-artifact@<sha> # v4.6.0
        with:
          name: plan-${{ matrix.stack.stack_id }}-${{ github.event.pull_request.head.sha }}
          path: |
            ${{ matrix.stack.path }}/tfplan
            ${{ matrix.stack.path }}/plan.json
            ${{ matrix.stack.path }}/plan.sha256
            ${{ matrix.stack.path }}/conftest.json
            ${{ matrix.stack.path }}/infracost.json
          retention-days: 30

      - name: Upload SARIF
        if: always()
        uses: github/codeql-action/upload-sarif@<sha> # v3.28.0
        with:
          sarif_file: ${{ runner.temp }}
          category: iac-${{ matrix.stack.stack_id }}

      - name: Build summary
        if: always()
        id: summary
        run: |
          .github/scripts/plan-summary.sh plan.json infracost.json conftest.json > "$RUNNER_TEMP/summary.json"
          .github/scripts/render-comment.sh "$RUNNER_TEMP/summary.json" "$RUNNER_TEMP/plan.txt" > "$RUNNER_TEMP/comment.md"

      - name: Sticky PR comment
        if: always()
        uses: marocchino/sticky-pull-request-comment@<sha> # v2.9.0
        with:
          header: iacp-${{ matrix.stack.stack_id }}
          path: ${{ runner.temp }}/comment.md

      - name: Report result to the platform
        if: always()
        uses: ./.github/actions/iacp-report
        with:
          event: ${{ job.status == 'success' && 'plan_ready' || 'plan_failed' }}
          stack-id: ${{ matrix.stack.stack_id }}
          payload: ${{ runner.temp }}/summary.json   # includes plan hash, add/change/destroy counts, cost delta, policy results

  # Single required status check, independent of matrix size
  pr-validate-result:
    needs: [detect, validate]
    if: always()
    runs-on: ubuntu-24.04
    steps:
      - run: |
          [ "${{ needs.detect.result }}" = "success" ] || exit 1
          [ "${{ needs.validate.result }}" = "success" ] || [ "${{ needs.validate.result }}" = "skipped" ] || exit 1
```

Notes:

- `-lock=false` on PR plans: plans are read-only and must not contend with an apply; the platform-level lock already prevents concurrent mutating requests on the same stack.
- The approved plan hash is stored by the API in `plan_summaries.plan_hash` keyed by `(request_id, head_sha)`. A new push to the PR invalidates the previous hash.
- Branch protection requires `pr-validate-result`, not each matrix leg, so check names stay stable.

#### Local-first implementation (current)

`.github/workflows/pr-validate.yml` follows the design above with these differences while [ADR-0009](adr/0009-local-first-localstack-and-local-backend.md) is in effect:

| Area | Target design | Today |
|---|---|---|
| AWS access | `iacp-plan` role through OIDC | LocalStack service container, dummy `test`/`test` credentials, generated LocalStack provider endpoints and the `local` backend; no `id-token: write` |
| Triggers | `environments/**` | Every PR, with no `paths` filter, so the required `pr-validate-result` check always reports. `detect` returns zero stacks quickly when nothing under `environments/` changed. Pipeline, policy and tool-version changes re-validate every stack |
| Module fetch | Published `modules/<name>/vX.Y.Z` tag | `verify-pins` before init and `verify-modules` after init (below) |
| Checkov | `checkov -d . --download-external-modules true` | Same, with `--skip-check CKV_TF_1`. Stacks pin exact release tags (enforced by `iacp.forbidden.module_source`), and the tag-to-commit binding that CKV_TF_1 protects is checked twice: by `verify-pins` against the remote, and by `verify-modules` against the checkout that init fetched |
| Policy context | `GET /v1/internal/budget-context` | `tools.stack_pipeline context`, built from the path-checked `iacp.stack.json`: environment, team, stack, no exceptions (WBS 6.4 replaces it) |
| Apply | Only in `apply.yml` | A hermetic round trip in the same job: apply the plan to the job's throwaway LocalStack, require an empty re-plan (`tools.stack_pipeline converged`, which tolerates only LocalStack dropping CloudWatch alarm tags), then destroy. Nothing outside the job is touched |
| PR comment | `marocchino/sticky-pull-request-comment` in the matrix job | The matrix job uploads `comment.md` / `summary.json`. A separate `comment` job, the only job with `pull-requests: write`, upserts one comment per stack. It writes the hidden `<!-- iacp-plan:<id> -->` marker itself from the validated artifact name, and drops any marker-looking line from the artifact body. Fork PRs are skipped (read-only token); their summary stays in the job summary |
| Artifacts | `tfplan`, `plan.json`, SARIF, Infracost | The rendered summary (`pr-validate-<id>`), the stack's SARIF (`sarif-<id>`, same-repo PRs only) and, only while Infracost is configured, a redacted plan for the cost job (`cost-input-<id>`, kept one day: `tools.stack_pipeline cost-input` builds it from an allow-list of the fields Infracost reads from a plan JSON, so `prior_state`, `resource_drift`, `output_changes`, variable values and configuration defaults, literal expressions and any key it does not know are dropped, and every value Terraform marks sensitive is nulled). `tfplan` and the full `plan.json` never leave the job. The SARIF upload is non-gating (`continue-on-error`); the summary upload stays fatal because the comment and report jobs read it. All uploads use `overwrite`, so re-running a leg replaces its artifacts instead of failing with 409. `overwrite` is delete-then-upload, so the folded summary is never uploaded over `pr-validate-<id>`: `cost-summary` uploads it as `summary-cost-<id>-<run attempt>` (the attempt keeps a re-run from reusing an earlier attempt's file) and the `comment` and `report` jobs prefer the current attempt's artifact, falling back to `pr-validate-<id>` when it is missing |
| Code scanning (WBS 6.1) | `upload-sarif` in the matrix job, `security-events: write` there | TFLint and Checkov keep their gating runs unchanged. TFLint runs a second, non-gating time with `--format sarif`; Checkov adds `--output sarif` to its gating run. `python -m tools.stack_pipeline sarif` rewrites the paths to repository paths: stack-relative files are joined to the stack root, and Checkov's downloaded-module files (`<download dir>/…/modules/<name>/vX.Y.Z/<path>`) become `<path>`, the module file in this repository (findings point at the default-branch file, so line numbers can drift from the pinned tag). Every rewrite passes one containment check: an absolute path, `.`/`..`, a path that leaves the repository, a module path outside `modules/` or a stack path outside the stack root becomes the stack's `main.tf`. It also drops each run's `automationDetails` and `versionControlProvenance`, which the PR's job could set, so only the upload's `category` applies. Invalid SARIF is never uploaded. A separate `code-scanning` matrix job, the only job with `security-events: write`, runs only checkout and the pinned `github/codeql-action/upload-sarif`, one category per tool and stack (`pr-validate/tflint/<id>`, `pr-validate/checkov/<id>`). Fork and Dependabot PRs are skipped (read-only token). The job is not part of `pr-validate-result`. These categories exist only on PRs, so code scanning shows every finding as new rather than diffing against `main`. `ci.yml` uploads the module scans the same way under `ci/tflint` and `ci/checkov`, on PRs and on `main`; its `terraform` job strips `automationDetails` and `versionControlProvenance` from both files first, with the runner's own `jq` (no PR code, no `pip install`) into a separate directory, and uploads only that directory, so a file that cannot be stripped is never uploaded raw and a same-repository PR cannot forge another category either. A path with `%` (escapes such as `%2e%2e`), a backslash or a drive letter (`C:`) is not kept as text, whatever branch it would take (stack-relative, repository-root or downloaded module): it also becomes the stack's `main.tf` |
| Cost (WBS 5.5) | Infracost `breakdown` + `diff`; budget gate in OPA `budget.rego` | Informational only, in two jobs after `validate`. `detect` reads only the repository variable `INFRACOST_ENABLED` (`cost_enabled`; it must be `true`) and never the secret, because `detect` runs PR-checkout Python; without the variable (today, and always on fork and Dependabot PRs) it logs a `::notice::`, both jobs are skipped and the comment has no cost line. Otherwise validate uploads the redacted plan (above). The `cost` job is the only job that receives the key: it checks out nothing and runs only Infracost `INFRACOST_VERSION` (sha256-verified like conftest) as `infracost breakdown --path plan.json --format json`, so no PR-controlled code runs next to the key. The `cost-summary` job has no secret; it runs `tools.stack_pipeline fold-cost`, which adds the monthly total and the priced and usage-based resource counts (and the diff, when Infracost reports one) to `summary.json` (`cost`) and one line of `comment.md`, and uploads them as `summary-cost-<id>-<run attempt>` for the `comment` and `report` jobs (the original summary artifact stays as the fallback). Both jobs have job-level `continue-on-error` and are not part of `pr-validate-result`; the cost never changes `ok`. No `diff` against `main` and no budget gate until WBS 8.5. Infracost Cloud upload is off (`INFRACOST_ENABLE_CLOUD=false`); its pricing API receives resource attributes, not the plan file |
| API report | `iacp-report` action (`plan_ready` / `plan_failed`) | A separate `report` matrix job, the only job with `id-token: write`, so the OIDC permission is never present while PR-controlled Terraform and providers run. It is OIDC only and never receives `IACP_INTERNAL_TOKEN`, because it runs the PR's own `tools/` and action code; without `vars.IACP_OIDC_AUDIENCE` the report fails as a warning. It downloads the stack's `summary.json` and reports stage `summary` for pipeline `validate`; the status comes from the summary, and a missing summary is reported as `failed`. `head_sha` is `github.sha` (the merge commit). Fork PRs are skipped. A report failure is a warning and never affects `pr-validate-result`. Skipped with a notice while `vars.IACP_API_URL` is empty. The validate report carries the `request_id` and `fencing_token` from `iacp.stack.json`, so it is accepted only while that token is current |

**Module pins.** A local relative source would be denied by `iacp.forbidden.module_source`, which cannot be waived, so stacks always keep the generator's exact pinned git source. Two steps tie that source to the commit recorded in `iacp.stack.json`.

`python -m tools.stack_pipeline verify-pins <stack>` runs before `terraform init`:

- **Published tag:** the tag on `origin` must resolve to the recorded commit, or the step fails.
- **Unpublished tag:** refused. The workflow-level `IACP_PIN_BOOTSTRAP` flag is `"false"`, now that module-release (WBS 3.6) publishes the tags; `modules/{s3,sqs,lambda}/v1.0.0` point at `681144a`. The `--allow-unpublished` path is kept, off by default, for the bootstrap that served tags before the first releases. Setting the flag to `"true"` passes that option, and then:
  - the recorded commit must already be on `origin/main`, and its `modules/<name>/module.yaml` must declare exactly that version;
  - the step pushes that one commit as the tag into a throwaway bare mirror;
  - the repository URL (with and without `.git`) is routed to the mirror through `GIT_CONFIG_*` `insteadOf` environment variables. No git config file is changed.

`python -m tools.stack_pipeline verify-modules <stack>` runs after `terraform init`:

- It reads `.terraform/modules/modules.json`. The stack's root module calls and the pins must match one to one, with none missing and none extra.
- Each source must be exactly `git::<repo>//modules/<name>?ref=<pinned tag>`, and nested calls inside platform modules may only use local paths.
- It checks that `HEAD` of each fetched checkout is the recorded commit. This uses an explicit `--git-dir`, so git cannot resolve the enclosing repository. It also catches a tag that moved between `ls-remote` and `init`.

The plan JSON therefore carries the real pinned source, and every policy is evaluated unchanged. Code from the PR branch is never served as a module. `make stack-plan` runs the same sequence locally and honours `IACP_PIN_BOOTSTRAP` (default `false`).

If the bootstrap is ever switched on again, switch it off as soon as the tags are published. Several commits on `main` can declare the same module version, and module-release tags the commit it releases, which may differ from the one a bootstrapped stack recorded. Re-render each such stack with the commit the published tag resolves to (`git ls-remote --tags origin`), or `verify-pins` fails on the mismatch. That happened with the smoke stack: it was bootstrapped at `06f6930` and re-rendered at `681144a` when the tag was published.

### 4.3 `apply.yml`

```yaml
name: apply

on:
  push:
    branches: [main]
    paths:
      - "environments/**"
  workflow_dispatch:            # used by backend for retries and destroy cleanup
    inputs:
      stacks:
        description: JSON array of stack paths to apply
        required: true
      reason:
        description: retry | rollback | manual
        required: true

permissions:
  contents: read

env:
  TF_IN_AUTOMATION: "true"
  TF_INPUT: "false"
  TF_PLUGIN_CACHE_DIR: ${{ github.workspace }}/.terraform.d/plugin-cache
  STATE_REGION: us-east-1

jobs:
  detect:
    runs-on: ubuntu-24.04
    outputs:
      matrix: ${{ steps.detect.outputs.matrix }}
      count:  ${{ steps.detect.outputs.count }}
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
        with: { fetch-depth: 0 }
      - id: detect
        run: |
          if [ "${{ github.event_name }}" = "workflow_dispatch" ]; then
            .github/scripts/changed-stacks.sh --paths '${{ inputs.stacks }}'
          else
            .github/scripts/changed-stacks.sh "${{ github.event.before }}" "${{ github.event.after }}"
          fi

  apply:
    needs: detect
    if: needs.detect.outputs.count != '0'
    runs-on: ubuntu-24.04
    timeout-minutes: 60
    environment: ${{ matrix.stack.env }}       # development | staging | production (required reviewers on production)
    permissions:
      contents: read
      id-token: write
    strategy:
      fail-fast: false
      max-parallel: 5
      matrix:
        stack: ${{ fromJSON(needs.detect.outputs.matrix) }}
    concurrency:
      group: stack-${{ matrix.stack.stack_id }}-apply   # one apply per stack, ever
      cancel-in-progress: false                          # never cancel a running apply
    defaults:
      run:
        working-directory: ${{ matrix.stack.path }}
    steps:
      - uses: actions/checkout@<sha> # v4.2.2

      - name: Load tool versions
        working-directory: .
        run: cat .github/tool-versions.env >> "$GITHUB_ENV"

      - uses: hashicorp/setup-terraform@<sha> # v3.1.2
        with:
          terraform_version: ${{ env.TERRAFORM_VERSION }}
          terraform_wrapper: false

      - uses: actions/cache@<sha> # v4.2.0
        with:
          path: ${{ env.TF_PLUGIN_CACHE_DIR }}
          key: tf-providers-${{ runner.os }}-${{ hashFiles(format('{0}/.terraform.lock.hcl', matrix.stack.path)) }}

      - name: Verify platform lock and fetch approved plan hash
        id: approved
        run: |
          RESP=$(curl -sSf -H "Authorization: Bearer $(${GITHUB_WORKSPACE}/.github/scripts/iacp-token.sh)" \
            "${{ vars.IACP_API_URL }}/v1/internal/requests/by-path?stack_path=${{ matrix.stack.path }}&head_sha=$GITHUB_SHA")
          # 409 if the commit does not belong to the request holding the stack lock; 412 if that request is not `approved`
          echo "hash=$(echo "$RESP" | jq -r .approved_plan_hash)" >> "$GITHUB_OUTPUT"
          echo "fencing_token=$(echo "$RESP" | jq -r .fencing_token)" >> "$GITHUB_OUTPUT"   # sent with every callback; stale tokens are rejected

      - uses: ./.github/actions/iacp-report
        with: { event: apply_started, stack-id: "${{ matrix.stack.stack_id }}" }

      - name: Configure AWS credentials (apply role)
        uses: aws-actions/configure-aws-credentials@<sha> # v4.0.2
        with:
          role-to-assume: arn:aws:iam::${{ matrix.stack.account_id }}:role/iacp-apply
          role-session-name: apply-${{ github.run_id }}-${{ matrix.stack.stack_id }}
          aws-region: ${{ env.STATE_REGION }}
          role-duration-seconds: 3600

      - name: terraform init
        run: mkdir -p "$TF_PLUGIN_CACHE_DIR" && terraform init -lockfile=readonly

      - name: Re-plan and verify against approved plan
        run: |
          if [ "${{ matrix.stack.desired_state }}" = "absent" ]; then MODE="-destroy"; else MODE=""; fi
          terraform plan $MODE -out=tfplan -no-color
          terraform show -json tfplan > plan.json
          ACTUAL=$(jq -S '[.resource_changes[] | {address, actions: .change.actions, after: .change.after}]' plan.json | sha256sum | cut -d' ' -f1)
          if [ "$ACTUAL" != "${{ steps.approved.outputs.hash }}" ]; then
            echo "::error::Plan differs from approved plan (approved=${{ steps.approved.outputs.hash }} actual=$ACTUAL). Re-validation required."
            echo '{"reason":"plan_mismatch"}' > "$RUNNER_TEMP/fail.json"
            exit 1
          fi

      - name: terraform apply
        run: terraform apply -no-color -auto-approve tfplan 2>&1 | tee "$RUNNER_TEMP/apply.log"

      - name: Collect outputs
        if: matrix.stack.desired_state == 'present'
        run: terraform output -json | jq 'with_entries(select(.value.sensitive == false) | .value = .value.value)' > "$RUNNER_TEMP/outputs.json"

      - name: Smoke / Terratest (ephemeral stacks)
        if: matrix.stack.ephemeral == true && matrix.stack.desired_state == 'present'
        working-directory: tests
        run: go test ./smoke/... -run "TestSmoke" -count=1 -timeout 15m -json \
               -args -stack-outputs="$RUNNER_TEMP/outputs.json" > "$RUNNER_TEMP/smoke.json"

      - name: Upload apply artifacts
        if: always()
        uses: actions/upload-artifact@<sha> # v4.6.0
        with:
          name: apply-${{ matrix.stack.stack_id }}-${{ github.sha }}
          path: |
            ${{ matrix.stack.path }}/plan.json
            ${{ runner.temp }}/apply.log
            ${{ runner.temp }}/outputs.json
          retention-days: 90

      - name: Report success
        if: success()
        uses: ./.github/actions/iacp-report
        with:
          event: applied
          stack-id: ${{ matrix.stack.stack_id }}
          payload: ${{ runner.temp }}/outputs.json

      - name: Report failure
        if: failure()
        uses: ./.github/actions/iacp-report
        with:
          event: apply_failed
          stack-id: ${{ matrix.stack.stack_id }}
          payload: '{"log_tail_artifact":"apply-${{ matrix.stack.stack_id }}-${{ github.sha }}"}'
```

Notes:

- `environment: production` makes the job wait for a required reviewer and changes the OIDC `sub` to `repo:acme/self-service-iac-platform:environment:production`; `iacp-apply` in the production account trusts **only** that subject (see [07-security-and-policy.md](07-security-and-policy.md)). The development/staging apply roles trust their matching environment subjects. Nothing else can assume an apply role.
- The job-level `concurrency` group serialises applies per stack across all workflow runs; the platform lock row in Postgres (checked via `requests/by-path`, which returns the lock's fencing token) serialises at request level ([ADR-0004](adr/0004-s3-native-locking-plus-platform-lock.md)).
- State locking during apply uses the S3 lockfile (`use_lockfile = true` in `backend.tf`).
- Applies are never cancelled automatically; a cancelled apply leaves a stale lockfile — see the runbook in [06-lifecycle-management.md](06-lifecycle-management.md#3-state-locking).

#### Local-first implementation (current)

`.github/workflows/apply.yml` (WBS 5.3) follows the design above with these differences while [ADR-0009](adr/0009-local-first-localstack-and-local-backend.md) is in effect:

| Area | Target design | Today |
|---|---|---|
| Triggers | `push` to `main` on `environments/**`; `workflow_dispatch` with a stack list | Same `push` trigger. `workflow_dispatch` takes one `stack_path`, plus optional `destroy`, `stack_id`, `request_id` and `approved_plan_hash`. Only `main` is applied: a `Require main` step fails a run from any other ref, and the apply job is guarded on `github.ref` again. The durable control is a GitHub Environment limited to `main` (WBS 6.6). A dispatched `stack_path` is shape-checked before it is used or echoed |
| Detection | `changed-stacks.sh` | `tools.stack_pipeline detect --base github.event.before --head github.sha`, with no `--full-run-prefix`: a pipeline change re-validates every stack in `pr-validate`, but apply only applies the stacks a push changed. A missing or all-zero `before` falls back to the parent commit, with a warning. A dispatched path goes through the same detector (fail closed) and must name an existing stack root |
| AWS access | `iacp-apply` via OIDC in GitHub Environment `<env>` | LocalStack service container with dummy `test`/`test` credentials. No GitHub Environment yet (WBS 6.6). `id-token: write` exists only on the apply job, and the API is reached with its OIDC token only; `IACP_INTERNAL_TOKEN` is never passed to `apply.yml` |
| Concurrency | `stack-<id>-apply` | `iacp-stack-<id>-apply`, or `iacp-stack-<id>-destroy` for a destroy dispatch; `cancel-in-progress: false`, `max-parallel: 5`. GitHub keeps only the newest pending job per group. For push applies that is fine, because the newest `main` contains every change. A queued destroy, though, would be replaced silently by a later push, so destroys get their own group. The platform lock then keeps an apply and a destroy of one stack apart: the second claim is `queued` and fails visibly. With no API, each job only touches its own LocalStack |
| Platform lock | `GET /v1/internal/requests/by-path` returns the lock's fencing token | `POST /v1/internal/stacks/{id}/lock` through `tools.stack_pipeline lock acquire`. `granted` or `held` returns the fencing token; `queued` fails the job. The token goes into every report, and the lock is released (`DELETE …/lock?request_id=&fencing_token=`) in an `always()` step. `attempted=true` is recorded before the claim, so the release also runs when the grant committed but every response was lost. Without a token, the release reads the lock (`GET …/lock`) and frees it only if this request holds it. The lock route accepts OIDC tokens only from `apply.yml`/`ttl-reaper.yml` on `main` via `push`, `workflow_dispatch` or `schedule` (`auth.RouteLocks`) |
| Lock target | Resolved by the API from the stack path and merge commit | `tools.stack_pipeline lock acquire --stack-path "$STACK_PATH" [--head-sha "$GITHUB_SHA" on push]` asks `GET /v1/internal/requests/by-path` for the request holding the stack lock, which must be `approved` or `applying` and, when its merge commit is recorded, must be the pushed commit (`auth.RouteRequestLookup`: `apply.yml` on `main`, `push` or `workflow_dispatch`). The ids and the approved plan hash become step outputs; the release step and the plan-hash check use them. A lookup that finds nothing fails the job before any lock claim or apply; `merge_not_recorded` (webhook lag) is retried for about 30 s first. The lookup always runs when a path is given, so `stack_id` and `request_id` dispatch inputs must agree with the API and the approved hash comes back either way. With the API unset, the claim is skipped with a notice. A dispatched `stack_id` is not otherwise checked against `stack_path` at claim time; the `plan` report is the binding check: the API resolves the stack by path and checks the token against that stack's lock, so a lock claimed on another stack gets `409` before apply |
| Module fetch | Published tag | `verify-pins` before init and `verify-modules` after init, as in `pr-validate` |
| Pre-apply gate | OPA on the plan with approvals | `conftest` with the same policies and the `context` from `iacp.stack.json` on the plan about to be applied |
| Plan-hash check | Re-plan hash must equal `approved_plan_hash` | Compared against the hash the platform recorded at approval (resolved through `requests/by-path` by the lock step) or the dispatched `approved_plan_hash`; when both exist they must agree. Both hashes must be 64 lowercase hex characters. With `vars.IACP_API_URL` set, a missing approved hash fails the job (fail closed, like the lock); only the local-first mode, with no API, applies with a notice. The hash is the same normalised `plan_hash` as the PR summary. Both hermetic plans start from an empty LocalStack, so an unchanged stack hashes the same in the PR and here |
| Reports | `apply_started`, `applied`, `apply_failed` | `iacp-report` stages `plan` (with the plan and policy blocks, `fail-on-error: "true"`), `apply` (or `destroy`, with `terraform_exit_code`), `smoke` (post-apply tests, below), `summary` (`job.status`, redacted outputs, `error` on failure) and, only when releasing the lock fails, `release`. The API moves the request and stack with the reports (docs/03 §4.1): the first accepted report moves an `approved` request to `applying`; a passed `summary` (or a failed one that only the smoke test explains) finishes the run as `succeeded` and the request as `applied` (stack `active`, or `destroyed` after a destroy, with outputs, `last_applied_sha` and health); a failed or cancelled `plan`, `apply`/`destroy` or `summary` finishes it as `failed` and the stack as `failed`. A failed `smoke` and any `release` report never finish a run. The terminal transition releases the lock and publishes the next queued request in the same transaction, so the lease is already gone when the workflow's own release step runs: `DELETE …/lock` with the request's own token answers `200` with `already_released: true`. Reports for a finished request are therefore accepted with the request's own token, but only for a run the API already knows (`runs.AcceptsReleasedToken`); they are stored as audit evidence (`request.run_report_ignored`), never merged into the finished run's stages, and change nothing. A failed `release` is audited (`request.lock_release_failed`, with the lock's current state), shown in the run and never flips the request. Any other token, or a run the API never saw, gets `409 stale_fencing_token` |
| Outputs | Uploaded `outputs.json` | Kept in the job. Only output names reach the step summary; the report carries values with sensitive ones redacted. `tfplan`, `plan.json` and state never leave the job |
| Destroy | `desired_state: absent` → `plan -destroy` | A `destroy` request (WBS 4.5) publishes an empty root marked `"lifecycle": "destroy"` in `iacp.stack.json`, so the PR plan and the push re-plan are both plain plans that destroy everything and need no special mode here. `workflow_dispatch` with `destroy: true` still runs `plan -destroy`, then applies it. The cleanup PR and the `destroyed` status belong to WBS 7.6 |
| Post-apply tests (WBS 5.7) | Smoke tests; Terratest for ephemeral stacks; failure → `health=degraded`, no auto-destroy | After a successful apply (not a destroy), `python -m tools.stack_pipeline smoke` checks the same job's LocalStack (it refuses an empty endpoint, a backslash, userinfo and any host other than loopback or `*.localstack.cloud`, so it can never reach real AWS while ADR-0009 is in force): every output the configuration declares (from `plan.json`) has a non-null value in `terraform output -json`; every resource in state that has `tags_all` carries `iacp:team`, `iacp:environment`, `iacp:stack` (from the stack path) and `iacp:managed-by=iacp`; and for S3 buckets, KMS keys, SQS queues, Lambda functions, IAM roles, log groups, metric alarms, SNS topics, Secrets Manager secrets and EventBridge rules, the same tags read back with read-only AWS CLI calls (preinstalled on the runner) against `AWS_ENDPOINT_URL`. Other tagged types are checked in state only and listed. A stack with no outputs and no tagged resources passes with "nothing to check". The expectations and the probe table are pure; the AWS CLI is an adapter. The result goes to the job summary and is reported as stage `smoke`. A failure fails the job (and so the `summary`, which is sent from `job.status`). The backend does not finish the run on a failed `smoke` stage: when the apply stage passed and the smoke test is the only failure, the run is `succeeded`, the request `applied` and the stack `active` with `health=degraded` (`runs.SmokeExplainsFailure`, migration 0011). `state.json` is deleted after the check. Terratest is not run here: `ci.yml` already runs the Terratest suite against LocalStack on every PR and push to `main` |

**Hermetic limit.** Every job starts a fresh LocalStack container, and the `local` backend writes state inside the job's workspace. Both are thrown away when the job ends. So today `apply.yml` proves that the merged stack applies cleanly from scratch, with the lock and the reports exercised. It does **not** keep resources or state between runs:

- every push re-creates the stack from nothing;
- an update or a `destroy` dispatch plans against empty state (a destroy is a no-op);
- outputs describe resources that no longer exist once the job ends.

Nothing in the workflow depends on that. When the generator switches to the S3 backend on a long-lived LocalStack (ADR-0009 decision 4), and later to real accounts, only `backend.tf`, the endpoint and the credentials step change. The lock, reports, plan-hash check and destroy path stay as they are.

### 4.4 `drift.yml`

Triggers: `schedule` (`0 2 * * *`, 02:00 UTC) and `workflow_dispatch` (optional `stacks` input: a JSON array of stack paths). There is no `pull_request` trigger, so a fork cannot run it with a token; every job also skips a fork (`github.event.repository.fork`), scan and report run only on `refs/heads/main`, and the list job fails loudly on any other ref. Default token `contents: read`; `id-token: write` exists only on the `report` job.

| Job | Permissions | What it does |
|---|---|---|
| `list` | `contents: read` | `python -m tools.stack_pipeline drift list`: the active stack roots under `environments/` (`lifecycle: active` in `iacp.stack.json`; a destroyed stack's marked root is skipped) as a matrix, at most 256 (warning when cut). The dispatch input is validated (existing, active stack paths) and never echoed. |
| `scan` (matrix) | `contents: read` | Per stack, `concurrency: iacp-stack-<id>-drift` (a newer run replaces an older scan). A LocalStack service container with the dummy credentials and the Terraform local backend, as in `apply.yml`. Pins, `init -lockfile=readonly` and installed-module verification run with `continue-on-error`, then `terraform plan -refresh-only -lock=false -detailed-exitcode`; `terraform show -json` of the plan feeds `drift scan`, which classifies the exit code (0 clean, 2 drift, 1 error) and writes the result file. A step that stopped the plan becomes an `error` result: a failed check is never drift. The result file (addresses, resource types, actions, the **names** of the changed attributes, a fingerprint of those) is the only artifact (`drift-<id>`, one day); the plan file and every attribute value stay in the job. |
| `report` | `contents: read`, `id-token: write` | One job, no Terraform and no module code, `concurrency: iacp-drift-report`. Downloads the result files as untrusted data, then `drift report`: `GET /v1/internal/drift/stacks` to learn which stacks the API wants checked now (settled, not locked by a request), then `POST /v1/internal/drift` for every scanned stack on that list, **each request with its own OIDC token** (the API refuses a reused `jti`). A scanned stack the API does not list (it became busy since) is skipped with a notice; a due stack with no result is a warning; a rejected report fails the job after the others were sent. With `vars.IACP_API_URL` empty the step prints a notice and sends nothing. |

The result file is validated strictly on both sides: `tools.stack_pipeline.drift.outcome_from_dict` refuses unknown fields, wrong types and an inconsistent outcome before anything is sent, and the API validates the report again (`drift.Validate`). The route class (`drift`, [03 §6.9](03-data-model-and-api.md#69-pipeline-runs-actions-callbacks)) accepts only `drift.yml` on main via `schedule` or `workflow_dispatch`, and the report must carry the calling run's own id, attempt and commit.

**What is and is not meaningful today (ADR-0009).** State is hermetic: no state file is committed, the local backend starts empty in every job and the LocalStack container is discarded. A refresh-only plan over an empty state has nothing to refresh, so every scan exits 0 and every report says `clean`. A scheduled run therefore proves the pipeline, not the infrastructure: listing, pins, init, the plan, classification, the result artifact, the API list, one OIDC token per report, the report and the stack-health transition. A `drifted` result needs state that survives between runs (the S3 backend once ADR-0009 ends, or a long-lived LocalStack with its state restored); until then the drifted path is covered by the backend and tools tests, which feed recorded refresh-only plans through the same code, and was run by hand against LocalStack (apply, change a bucket out of band, `plan -refresh-only` exits 2). Provider noise is real: right after a clean `apply`, `aws_s3_bucket` of the `s3` module already refreshes `policy`, `versioning`, `lifecycle_rule` and `server_side_encryption_configuration` (the resource's deprecated inline attributes, filled in by the separate sub-resources), so a persisted-state scan reports that bucket as drifted until the module hides those attributes (docs/06 §4, provider-side noise).

A second, non-refresh plan (`terraform plan -detailed-exitcode`) for stacks with unapplied config changes on `main` (the `pending_changes` label) is not built yet; see [06-lifecycle-management.md](06-lifecycle-management.md#4-drift-detection).

### 4.5 `ttl-reaper.yml`

The primary TTL path is event-driven (EventBridge Scheduler → worker → destroy request). This workflow is the backstop: it finds expired stacks in Git that the platform has not yet acted on.

```yaml
name: ttl-reaper
on:
  schedule: [{ cron: "0 * * * *" }]           # hourly
  workflow_dispatch: {}
permissions: { contents: read, id-token: write }
jobs:
  reconcile:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
      - name: Find expired ephemeral stacks
        run: |
          NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
          find environments -name iacp.stack.json -print0 \
            | xargs -0 jq -c --arg now "$NOW" \
                'select(.ephemeral == true and .desired_state == "present" and .expires_at < $now) | {stack_id, expires_at}' \
            | jq -s --arg run "$GITHUB_RUN_ID" '{event: "ttl_reconcile", run_id: $run, data: {expired: .}}' > expired.json
          cat expired.json
      - name: Ask platform to reconcile
        run: |
          curl -sSf -X POST -H "Authorization: Bearer $(.github/scripts/iacp-token.sh)" \
            -H "Content-Type: application/json" --data-binary @expired.json \
            "${{ vars.IACP_API_URL }}/v1/runs"   # event: ttl_reconcile
```

The API is idempotent: stacks already in `destroying`, or with an open destroy request, are ignored. Warnings go out 24 h before expiry (scheduler-driven). In non-production environments only, the platform bot auto-approves destroy PRs created by TTL expiry; production stacks cannot be ephemeral. This workflow never runs `terraform destroy` itself; destruction always flows through a destroy request and PR so it is reviewed, audited and policy-checked.

### 4.6 `module-release.yml`

**Implemented (WBS 3.6, 3.7; local-first, ADR-0009).** [`.github/workflows/module-release.yml`](../.github/workflows/module-release.yml) uses `tools/module_release` (Python, `python-hcl2`) for every step that needs logic:

| Job | Runs on | What it does | Permissions |
|---|---|---|---|
| `detect` | every PR; push to `main` touching the release paths | On a PR, plain `git diff` first checks whether `modules/`, `tools/module_release/`, `tools/requirements.txt` or the workflow changed; if not, it emits an empty matrix without installing Python. Otherwise `python -m tools.module_release detect --base <merge-base or push before> --include-untagged` emits a JSON matrix of modules changed in the diff, plus modules whose declared `metadata.version` has no tag yet. On push it also passes `--released-tags` (from `gh release list`) so modules tagged without a GitHub release are retried. An unknown base (for example the all-zero SHA of a new branch) selects every module. | `contents: read` |
| `gate` (matrix) | PR and push | `python -m tools.module_release gate <module>`: the interface-diff SemVer gate of [04 section 5.2](04-module-registry.md#52-semver-rules-for-infrastructure-modules). The result goes to the job summary. | `contents: read` |
| `semver gate result` | PR and push | One stable check for branch protection. The `pull_request` trigger has no `paths:` filter, so it reports on every PR; it passes when the gate passed or no module needed it. | none |
| `release` (matrix) | `push` or `workflow_dispatch` on `main` | `info` reads version and tag. Creates the tag `modules/<name>/v<version>` at `GITHUB_SHA` through the Git refs API if it is missing, and never moves an existing one. Creates a GitHub release (marked prerelease for `-rc.N` versions) with notes from `notes`: the CHANGELOG section, or a generated diff summary. Then runs `index --skip-if-unconfigured` against `vars.IACP_API_URL` with `secrets.IACP_INTERNAL_TOKEN`, using the commit the tag points at (annotated tags are peeled). Skipped only when both are unset. | `contents: write` (only this job) |

Every `run:` step uses `shell: bash` (`-eo pipefail`). Static checks, `terraform test` and Terratest are not repeated here; they run on every PR in `ci.yml`. Not yet implemented from the target below: the GitHub App token and `module-release` environment (the `GITHUB_TOKEN` creates tags for now), the upgrade-plan test, OIDC authentication for the index call (the backend already accepts OIDC from `module-release.yml` on main via `push` or `workflow_dispatch`; the step still sends the shared token), and provenance attestation. `workflow_dispatch` on `main` re-runs the release for every module. Locally, `make module-gate` runs the gate for every module.

Target design:

```yaml
name: module-release
on:
  pull_request:
    paths: ["modules/**", "tests/modules/**"]
  push:
    branches: [main]
    paths: ["modules/**"]
permissions: { contents: read }
jobs:
  detect:
    runs-on: ubuntu-24.04
    outputs: { modules: "${{ steps.d.outputs.modules }}" }
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
        with: { fetch-depth: 0 }
      - id: d
        run: .github/scripts/changed-modules.sh   # emits ["s3","sqs"] from changed modules/<name>/ paths
  test:
    needs: detect
    runs-on: ubuntu-24.04
    strategy: { fail-fast: false, matrix: { module: "${{ fromJSON(needs.detect.outputs.modules) }}" } }
    services:
      localstack:
        image: localstack/localstack:4.0@sha256:<digest>
        ports: ["4566:4566"]
        env: { SERVICES: "s3,sqs,kms,iam,lambda,logs,sts,cloudwatch" }
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
        with: { fetch-depth: 0 }
      - uses: hashicorp/setup-terraform@<sha> # v3.1.2
        with: { terraform_version: "1.10.5", terraform_wrapper: false }
      - uses: actions/setup-go@<sha> # v5.2.0
        with: { go-version-file: tests/go.mod, cache-dependency-path: tests/go.sum }
      - run: pip install terraform-local==0.20.0 checkov==3.2.334
      - name: Static checks
        working-directory: modules/${{ matrix.module }}
        run: |
          terraform fmt -check -recursive
          terraform init -backend=false && terraform validate
          tflint --config ../_shared/.tflint.hcl
          checkov -d . --external-checks-dir ../../policies/checkov --quiet
          terraform test
      - name: Manifest schema, parity and SemVer checks
        run: |
          .github/scripts/validate-manifest.sh modules/${{ matrix.module }}
          .github/scripts/semver-gate.sh ${{ matrix.module }}   # interface diff + upgrade-plan test vs previous tag
      - name: Terratest (LocalStack)
        working-directory: tests
        run: go test -tags integration ./modules/... -run "Test$(echo ${{ matrix.module }} | sed -E 's/(^|-)(.)/\U\2/g')" -count=1 -timeout 20m
  release:
    needs: [detect, test]
    if: github.event_name == 'push'
    runs-on: ubuntu-24.04
    environment: module-release
    permissions: { contents: read, id-token: write }
    strategy: { matrix: { module: "${{ fromJSON(needs.detect.outputs.modules) }}" } }
    steps:
      - uses: actions/checkout@<sha> # v4.2.2
      - id: app-token
        uses: actions/create-github-app-token@<sha> # v1.11.0
        with:
          app-id: ${{ vars.IACP_APP_ID }}
          private-key: ${{ secrets.IACP_APP_PRIVATE_KEY }}
      - name: Tag and create release
        env: { GH_TOKEN: "${{ steps.app-token.outputs.token }}" }
        run: |
          V=$(yq -r .metadata.version modules/${{ matrix.module }}/module.yaml)
          TAG="modules/${{ matrix.module }}/v$V"
          gh api repos/${{ github.repository }}/git/refs -f ref="refs/tags/$TAG" -f sha="$GITHUB_SHA"
          .github/scripts/changelog-section.sh modules/${{ matrix.module }}/CHANGELOG.md "$V" > notes.md
          gh release create "$TAG" --title "${{ matrix.module }} v$V" --notes-file notes.md --verify-tag
          echo "{\"module\":\"${{ matrix.module }}\",\"version\":\"$V\",\"commit_sha\":\"$GITHUB_SHA\"}" > index.json
      - name: Index into catalog
        run: |
          curl -sSf -X POST -H "Authorization: Bearer $(.github/scripts/iacp-token.sh)" \
            -H "Content-Type: application/json" --data-binary @index.json \
            "${{ vars.IACP_API_URL }}/v1/internal/catalog/index"
```

The tag push fails if the tag already exists, so re-running the job cannot move a released version. See [04-module-registry.md](04-module-registry.md#9-release-flow-and-catalog-indexing).

## 5. Repository protection

### 5.1 Branch ruleset for `main`

| Setting | Value |
|---|---|
| Require pull request | yes; 1 approval (2 for paths owned by `@acme/security`) |
| Require review from Code Owners | yes |
| Dismiss stale approvals on new commits | yes |
| Require approval of most recent push | yes (author cannot self-approve after pushing) |
| Required status checks | The job names in `.github/rulesets/main-branch.json`: `actionlint (workflows)`, `terraform (fmt, validate, tflint, checkov)`, `policy (opa, conftest, checkov checks)`, `module tests (terraform test on LocalStack)`, `module tests (terraform test, mocked providers)`, `module tests (provider floor, mocked providers)`, `go (backend)`, `terratest (LocalStack)`, `python (tools)`, `portal (lint, typecheck, test, build)`, `ci-result`, `pr-validate-result` and `semver gate result` (module-release; reports on every PR). A job skipped by its `detect` gate reports `skipped`, which satisfies the check, and so does a job skipped because `detect` failed; `ci-result` (`needs` every gating job of `ci.yml`, `if: always()`) therefore requires `detect` to have succeeded and every other job to have succeeded or been skipped, so a failed `detect` cannot merge. `make required-checks-check` fails when a name here is not a job name |
| Require branches up to date before merging | yes (guarantees the PR plan was computed against current `main`) |
| Require linear history | yes (squash merge only) |
| Require signed commits | yes (GitHub App commits are signed by GitHub) |
| Block force pushes / deletions | yes |
| Bypass actors | none on `main`. The GitHub App is **not** a bypass actor; it merges only via the normal PR rules |

Tag ruleset `modules/**`: creation restricted to the platform GitHub App; update and deletion blocked for everyone.

### 5.2 CODEOWNERS

```
# Default
*                                         @acme/platform-team

# Generated stacks (lines generated per team; CODEOWNERS is last-match-wins, so order matters)
/environments/development/                @acme/self-service-iac-platform-approvers             # dev: auto-approved by the platform after green checks
/environments/staging/payments/           @acme/payments-maintainers
/environments/staging/search/             @acme/search-maintainers
/environments/production/payments/        @acme/payments-maintainers   # SRE gate is the production Environment reviewer
/environments/production/search/          @acme/search-maintainers

# Modules
/modules/s3/                              @acme/platform-storage @acme/platform-team
/modules/sqs/                             @acme/platform-messaging @acme/platform-team
/modules/lambda/                          @acme/platform-compute @acme/platform-team

# Security-critical
/policies/                                @acme/security
/.github/                                 @acme/platform-team @acme/security
/modules/**/iam.tf                        @acme/security
/backend/internal/generator/              @acme/platform-team
```

The backend generates the per-team `environments/*/<team>/` lines from the `teams` table and keeps CODEOWNERS in sync through a platform PR whenever a team is created.

For **development** stacks, the API auto-approves on behalf of the request when all checks pass and the requester holds `developer` on the team (an approving review is submitted by the dedicated `iacp-approver` machine user — GitHub Apps cannot be code owners — which is a CODEOWNER only for `environments/development/**` and whose token is held in Secrets Manager and used solely by the worker). Staging requires a human team `maintainer`; production requires a team `maintainer` **and** a GitHub Environment reviewer from `@acme/sre`.

### 5.3 GitHub Environments

| Environment | Required reviewers | Wait timer | Deployment branches | Secrets / vars |
|---|---|---|---|---|
| `development` | none | 0 | `main` only | `vars.AWS_ACCOUNT_ID` |
| `staging` | none (PR review is the gate) | 0 | `main` only | `vars.AWS_ACCOUNT_ID` |
| `production` | 1 of `@acme/sre`, prevent self-review | 5 min | `main` only | `vars.AWS_ACCOUNT_ID` |
| `module-release` | none | 0 | `main` only | `IACP_APP_PRIVATE_KEY` |

## 6. Caching and performance

| What | Mechanism | Key | Notes |
|---|---|---|---|
| Terraform providers | `TF_PLUGIN_CACHE_DIR` + `actions/cache` | OS + hash of stack `.terraform.lock.hcl` | Generator writes lock files with `terraform providers lock -platform=linux_amd64 -platform=darwin_arm64`, so `-lockfile=readonly` verifies checksums even from cache. `pr-validate.yml` and `apply.yml` use the same path and key; a cache saved by `apply.yml` on `main` is restorable by every PR. `apply.yml` runs no TFLint, so it has no plugin cache |
| TFLint plugins | `TFLINT_PLUGIN_DIR` + `actions/cache` | OS + TFLint version + hash of `.tflint.hcl` | |
| Python tools (Checkov, `tools/stack_pipeline`) | `actions/setup-python` pip cache | `policies/checkov/requirements.txt` | |
| Go modules (Terratest) | `actions/setup-go` built-in cache | `tests/go.sum` | |
| Checkov / conftest / Infracost binaries | Pre-built runner image `ghcr.io/acme/self-service-iac-platform-ci@sha256:...` (optional) | image digest | Cuts ~40s per job |
| Terraform modules (Git) | Not cached | — | Always fetched by tag so the SHA integrity check in [04-module-registry.md](04-module-registry.md#8-how-consumers-pin-versions) is meaningful |
| Infracost pricing | Infracost Cloud Pricing API cache | — | Self-hosted pricing API is an option for air-gapped setups |

Additional performance rules: matrix fan-out only over changed stacks; `max-parallel` caps AWS API pressure; PR concurrency cancels superseded plans; typical PR wall time target is under 4 minutes for a single stack.

Concurrency groups: `pr-validate-<pr>` for the whole run, and `iacp-stack-<id>-plan-pr<pr>` per matrix leg. Both cancel in progress, so a new push replaces stale plans of the same stack. `apply.yml` (WBS 5.3) will use a non-cancelling `iacp-stack-<id>` group so applies to one stack queue rather than overlap.

## 7. Terratest: `modules/s3` against LocalStack

Fixture `tests/modules/fixtures/s3/main.tf`:

```hcl
variable "name" { type = string }

module "s3" {
  source      = "../../../../modules/s3"
  name        = var.name
  team        = "platform"
  environment = "development"
  tags        = { purpose = "terratest" }
}

output "id"          { value = module.s3.id }
output "arn"         { value = module.s3.arn }
output "kms_key_arn" { value = module.s3.kms_key_arn }
```

`tests/modules/s3_test.go`:

```go
//go:build integration

package modules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func localstackEndpoint() string {
	if v := os.Getenv("LOCALSTACK_ENDPOINT"); v != "" {
		return v
	}
	return "http://localhost:4566"
}

func newS3Client(t *testing.T) *s3.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	require.NoError(t, err)
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(localstackEndpoint())
		o.UsePathStyle = true
	})
}

func TestS3(t *testing.T) {
	t.Parallel()

	name := fmt.Sprintf("tt-%s", strings.ToLower(random.UniqueId()))
	opts := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:    "fixtures/s3",
		TerraformBinary: "tflocal", // rewrites provider endpoints to LocalStack
		Vars:            map[string]interface{}{"name": name},
		EnvVars: map[string]string{
			"AWS_ACCESS_KEY_ID":     "test",
			"AWS_SECRET_ACCESS_KEY": "test",
			"AWS_DEFAULT_REGION":    "us-east-1",
		},
	})
	defer terraform.Destroy(t, opts)
	terraform.InitAndApply(t, opts)

	// Idempotency: a second plan must be empty.
	exit := terraform.PlanExitCode(t, opts)
	assert.Equal(t, 0, exit, "module must converge in one apply")

	bucket := terraform.Output(t, opts, "id")
	kmsArn := terraform.Output(t, opts, "kms_key_arn")
	assert.Equal(t, "platform-development-"+name, bucket, "naming convention")

	client := newS3Client(t)
	ctx := context.Background()

	t.Run("encrypted with CMK", func(t *testing.T) {
		enc, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: &bucket})
		require.NoError(t, err)
		rule := enc.ServerSideEncryptionConfiguration.Rules[0]
		assert.Equal(t, types.ServerSideEncryptionAwsKms, rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
		assert.Equal(t, kmsArn, aws.ToString(rule.ApplyServerSideEncryptionByDefault.KMSMasterKeyID))
		assert.True(t, aws.ToBool(rule.BucketKeyEnabled))
	})

	t.Run("public access fully blocked", func(t *testing.T) {
		pab, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: &bucket})
		require.NoError(t, err)
		c := pab.PublicAccessBlockConfiguration
		assert.True(t, aws.ToBool(c.BlockPublicAcls))
		assert.True(t, aws.ToBool(c.BlockPublicPolicy))
		assert.True(t, aws.ToBool(c.IgnorePublicAcls))
		assert.True(t, aws.ToBool(c.RestrictPublicBuckets))
	})

	t.Run("versioning enabled", func(t *testing.T) {
		v, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &bucket})
		require.NoError(t, err)
		assert.Equal(t, types.BucketVersioningStatusEnabled, v.Status)
	})

	t.Run("TLS-only bucket policy", func(t *testing.T) {
		p, err := client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: &bucket})
		require.NoError(t, err)
		var doc struct {
			Statement []struct {
				Sid       string
				Effect    string
				Condition map[string]map[string]interface{}
			}
		}
		require.NoError(t, json.Unmarshal([]byte(aws.ToString(p.Policy)), &doc))
		found := false
		for _, s := range doc.Statement {
			if s.Effect == "Deny" && s.Condition["Bool"]["aws:SecureTransport"] != nil {
				found = true
			}
		}
		assert.True(t, found, "expected Deny on aws:SecureTransport=false")
	})

	t.Run("mandatory tags", func(t *testing.T) {
		tags, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: &bucket})
		require.NoError(t, err)
		got := map[string]string{}
		for _, tg := range tags.TagSet {
			got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
		assert.Equal(t, "platform", got["iacp:team"])
		assert.Equal(t, "development", got["iacp:environment"])
		assert.Equal(t, "s3", got["iacp:module"])
		assert.Equal(t, "terratest", got["purpose"])
	})
}

func TestS3RejectsForceDestroyInProduction(t *testing.T) {
	t.Parallel()
	opts := &terraform.Options{
		TerraformDir:    "fixtures/s3-prod-force-destroy",
		TerraformBinary: "tflocal",
	}
	_, err := terraform.InitAndPlanE(t, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force_destroy cannot be enabled in production")
}
```

Run locally with the compose stack from the repo root:

```bash
docker compose up -d localstack
cd tests && go test -tags integration ./modules/... -run TestS3 -count=1 -v
```

CI passes `-tags integration` explicitly (see `module-release.yml`). LocalStack covers S3, SQS, KMS, IAM, Lambda and CloudWatch Logs for the MVP modules; modules needing high-fidelity behaviour (RDS, ElastiCache, ECS) run their Terratest suite nightly against the `development` sandbox account using the `iacp-apply` role with a `terratest-*` name prefix and a 2-hour TTL tag.
