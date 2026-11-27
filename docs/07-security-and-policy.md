# 07 — Security and Policy

> Purpose: define the platform's threat model, identity and privilege design, secrets and supply-chain controls, policy-as-code (OPA/Rego and Checkov), the policy exception workflow, RBAC, and how controls map to audit evidence.

## Contents

1. [Security principles](#1-security-principles)
2. [Threat model (STRIDE)](#2-threat-model-stride)
3. [Least-privilege IAM](#3-least-privilege-iam)
4. [Secrets handling](#4-secrets-handling)
5. [Supply chain](#5-supply-chain)
6. [Policy as code](#6-policy-as-code)
7. [RBAC matrix](#7-rbac-matrix)
8. [Policy exception workflow](#8-policy-exception-workflow)
9. [Compliance and audit evidence](#9-compliance-and-audit-evidence)

Related: [01-system-architecture.md](01-system-architecture.md) · [02-aws-infrastructure.md](02-aws-infrastructure.md) · [03-data-model-and-api.md](03-data-model-and-api.md) · [05-automation-pipeline.md](05-automation-pipeline.md) · [06-lifecycle-management.md](06-lifecycle-management.md)

---

## 1. Security principles

1. **No standing write credentials.** Only GitHub Actions jobs on `main` (or bound to a GitHub Environment) can obtain write credentials to workload accounts, for ≤ 1 h, via OIDC.
2. **Reviewed code is applied code.** Apply runs from merged `main` and verifies the plan hash approved in the PR.
3. **Defence in depth on every change:** JSON-Schema validation → TFLint → Checkov → OPA on plan → Infracost budget → human review → environment approval (prod) → permission boundary + SCP at runtime.
4. **Control plane ≠ execution plane.** A compromised backend can propose but not apply.
5. **Everything attributable.** Every action → hash-chained `audit_events`, PR history, and CloudTrail events carrying `apply-<run_id>-<stack_id>` role session names.

## 2. Threat model (STRIDE)

Assets: workload account integrity, Terraform state (may contain secrets), GitHub App private key, OIDC trust, audit log integrity, module source integrity.

Trust boundaries: (A) browser ↔ portal/API; (B) GitHub ↔ API (webhooks, callbacks); (C) API ↔ GitHub API (App); (D) Actions runner ↔ AWS STS / workload accounts; (E) Actions runner ↔ state bucket; (F) module source (git tags) ↔ runner; (G) third-party actions/providers ↔ runner.

| Component | S — Spoofing | T — Tampering | R — Repudiation | I — Information disclosure | D — Denial of service | E — Elevation of privilege |
|---|---|---|---|---|---|---|
| **Portal / API** | Stolen session → impersonation. *M:* GitHub OAuth + PKCE, opaque hashed server-side sessions (8 h, 1 h idle, revocable), HttpOnly/SameSite cookies, per-session CSRF token, re-auth for prod destroy; org SSO enforced on GitHub. | Request body tampering to bypass schema. *M:* server-side JSON Schema validation, reserved inputs not overridable, generator is pure + golden-tested. | User denies requesting a change. *M:* audit event per action with actor, IP, request ID; PR authored by bot but co-authored-by trailer + request link. | IDOR across teams. *M:* RBAC middleware on every route, team scoping in SQL (`WHERE team_id = ANY($teams)`), 404 for invisible resources. | Request floods. *M:* WAF rate rules, per-user rate limits, `POST /requests` quota, SQS buffering. Metrics endpoint as a probe or cardinality bomb. *M:* `/metrics` answers only direct loopback peers or `IACP_METRICS_TOKEN` holders (assumes no same-host proxy relays outside traffic from loopback without forwarding headers), and labels by route pattern and a fixed method set (`OTHER` otherwise), never raw values. | Developer escalates to maintainer to self-approve prod. *M:* roles derived from GitHub teams only, sync every 15 min + on webhook; author cannot approve own PR (branch protection). |
| **Webhook ingester** | Forged webhook advancing state (e.g. fake approval). *M:* HMAC-SHA256 signature, constant-time compare; state transitions re-verified via GitHub API (review state, merge SHA) before acting. | Replay of old delivery. *M:* `X-GitHub-Delivery` dedupe; optimistic status checks. | — | Payload logs leaking data. *M:* log only IDs, not bodies. | Webhook storm. *M:* 202 + SQS, WAF rate limit, IP allowlist from GitHub meta. | — |
| **Pipeline callbacks** | Forged plan result ("all green"). *M:* GitHub OIDC JWT verified (iss, aud, repository_id, job_workflow_ref allowlist, run_id match), fencing token. | Altered plan hash. *M:* hash also recomputed in `apply.yml` from fresh plan; API only stores, apply compares to value bound at approval. | — | — | — | Callback from PR workflow claiming apply. *M:* per-endpoint `job_workflow_ref` + `ref=refs/heads/main` requirement. |
| **GitHub App** | Key theft → bot impersonation. *M:* private key in Secrets Manager (KMS CMK), only api/worker task roles can read, rotated 90 d, App permissions minimal (contents:write, pull_requests:write, actions:write, checks:read, metadata:read, members:read). | Bot pushes directly to `main`. *M:* branch protection applies to the App (no bypass list), required checks + CODEOWNERS. | — | Repo read exposure. *M:* App installed on `acme/self-service-iac-platform` only. | GitHub rate limits. *M:* backoff, ETags, budget alarms. | App merges its own PRs. *M:* App has no `administration` permission; required human review. |
| **GitHub Actions workflows** | Captured OIDC token replayed against the API. *M:* `jti` claimed once until `exp` + leeway in `oidc_jti` (shared by replicas); a second use is a 401; an outage of that store is a 503 (fail closed, retryable). Fork PR assumes AWS role. *M:* private repo, forking disabled; trust policy pins `repository_id` + `job_workflow_ref`; plan role only for PRs. | Malicious PR edits workflow to run apply. *M:* `job_workflow_ref` must be `@refs/heads/main` for apply; CODEOWNERS on `.github/**` = platform-security; workflows pinned actions by SHA. | — | Secrets in logs / plan output. *M:* `sensitive` outputs, `::add-mask::`, plan JSON redaction step before upload, no `TF_LOG` in CI. | Runner exhaustion. *M:* concurrency groups, path filters, timeouts (plan 20 min, apply 60 min). | PR changes modules to exfiltrate via plan (`external` data source, `local-exec`). *M:* TFLint/OPA deny `data.external`, `provisioner`, `http` data source with non-allowlisted hosts; plan role read-only with deny on data-plane reads. |
| **Terraform execution / workload accounts** | Role assumed outside pipeline. *M:* OIDC-only trust; EventBridge alert on `AssumeRole*` of `iacp-apply` without OIDC. | Console changes (drift). *M:* humans read-only via SSO; nightly drift; SCPs. | Who applied what. *M:* session name `apply-<run_id>-<stack_id>`, per-account CloudTrail trails → Object Lock archive. | Over-broad resource policies. *M:* OPA/Checkov (no public S3, no `*` principals), Access Analyzer. | Accidental mass destroy. *M:* OPA denies destroy of stateful resources in prod without explicit flag + maintainer; `prevent_destroy` in modules for data stores. | Created role with admin rights. *M:* `iacp-boundary` required by SCP and apply role; `iam:PassRole` restricted to `/iacp/` path. |
| **State bucket** | Other principals read state. *M:* bucket policy per-env prefix, `aws:PrincipalOrgID`, KMS key policy with encryption-context per env. | State corruption / rollback attack. *M:* versioning, Object Ownership enforced, CRR, delete denied on `.tfstate`. | — | State contains secrets. *M:* SSE-KMS CMK, humans have no decrypt, modules use Secrets Manager refs / `manage_master_user_password`. | Lock contention. *M:* S3 lockfile + platform lock queue. | — |
| **Module registry (git tags)** | Spoofed module source. *M:* modules only from `github.com/acme/self-service-iac-platform` (OPA + TFLint rule on `source`); tag protection rules on `modules/*/v*`. | Tag moved to malicious commit. *M:* tag protection; generator records tag→SHA and `apply.yml` verifies SHA; release only via `module-release.yml`. | Who released. *M:* release audit event + signed tags (gitsign). | — | — | Module adds IAM wildcard. *M:* module PR review by owners + security, Checkov on examples, OPA on every consumer plan. |
| **Database** | DB credential theft. *M:* IAM DB auth for app, no static password in env; SG only from app tasks. | Audit tampering. *M:* append-only triggers, INSERT/SELECT-only grant, hash chain + S3 anchors. | — | Data exposure. *M:* encryption at rest (CMK), TLS enforced, no PII beyond GitHub login/email. | Connection exhaustion. *M:* pgxpool limits, RDS Proxy if needed. | SQL injection. *M:* sqlc parameterised queries only. |
| **Third-party actions / providers** | Typosquatted action. *M:* allowlist of actions at org level. | Compromised action/provider release. *M:* SHA-pinned actions, provider lockfile with hashes, Dependabot with review. | — | Action exfiltrates OIDC token. *M:* `id-token: write` only on jobs that need it; minimal `permissions:` per job. | — | — |

## 3. Least-privilege IAM

| Principal | Where | Can | Cannot | Notes |
|---|---|---|---|---|
| `iacp-plan` | each workload account | Read-only describe/list/get-config; read own-env state prefix (plans use `-lock=false`) | Data-plane reads (object get, secret values, DynamoDB items, SSM decrypt); any write | Trust: PR `pr-validate.yml`, `drift.yml@main`. See [02-aws-infrastructure.md](02-aws-infrastructure.md#51-iacp-plan-trust-policy-per-workload-account). |
| `iacp-apply` | each workload account | Create/update/delete catalog services; IAM roles under `/iacp/` with boundary; own-env state RW | Users/keys, OIDC providers, Organizations, CloudTrail stop, remove boundary | Trust: `apply.yml`/`ttl-reaper.yml` on `main` in the matching GitHub Environment. |
| `iacp-boundary` | managed policy | Caps roles created by stacks | See [02-aws-infrastructure.md](02-aws-infrastructure.md#6-permission-boundary) | Required by SCP on role creation. |
| `iacp-test` | sandbox account | Full catalog services in sandbox | Anything outside sandbox | `module-release.yml` Terratest; account nuked nightly with `aws-nuke`. |
| `iacp-api-task` / `iacp-worker-task` | platform | SQS, specific secrets, audit anchor PutObject, `rds-db:connect` | Anything in workload accounts; state bucket | |
| `iacp-breakglass` | each account | Admin | — | SSO permission set, MFA, 1 h, 2-person approval in IdP, P1 alert on use, post-incident reconcile PR mandatory. |
| Humans (SSO) | workload accounts | `ReadOnlyAccess` (minus data-plane) | Writes | Writes only via the platform or break-glass. |

Review cadence: IAM Access Analyzer unused-access findings reviewed monthly; `iacp-apply` policy diffed on every change via PR with security CODEOWNER.

## 4. Secrets handling

- **Platform secrets** (GitHub App key, webhook secret, OAuth client secret, JWT signing keys, Slack webhook) live in Secrets Manager encrypted with `alias/iacp-app`; injected into ECS tasks via `secrets:` (valueFrom ARN), never baked into images or env files. Rotation: JWT keys 90 d (kid-based dual validation), webhook secret 180 d, GitHub App key 90 d (two active keys during rotation).
- **No AWS secrets in GitHub.** Repository/organisation secrets contain no AWS keys; only non-secret variables (`IACP_API_URL`, account IDs). Infracost API key is the only CI secret (scoped to `pr-validate` environment). Today it is the optional repository secret `INFRACOST_API_KEY` (not set yet; the cost step skips with a notice), switched on by the repository variable `INFRACOST_ENABLED=true`: pr-validate's `detect` job runs PR-checkout Python, so it reads only the variable and never the secret. The secret is passed only to pr-validate's `cost` job, which checks out nothing and runs only the checksum-verified Infracost binary on a redacted plan from the validate job. No PR-controlled Terraform, provider or `tools/` code runs in that job, so it cannot redirect the key through `GITHUB_ENV`, files or a leftover process. Fork and Dependabot PRs never get it. The redacted plan is PR-produced data: it can make Infracost price something else, not read the key. Residual risk: a same-repository PR can edit the workflow itself. The key is low value (price lookups only), and a GitHub Environment limited to reviewed refs is the durable control.
- **Workload secrets** are never module inputs in plain text. Modules accept secret *references* (`secret_arn`), create secrets with random values and `ignore_changes` on value, or use native managed passwords (`manage_master_user_password = true` for RDS). JSON Schema marks fields `"x-iacp-sensitive": true`; the API rejects values that look like secrets (high-entropy detector + `gitleaks` rules) in non-sensitive fields.
- **State** is the residual risk: encrypted with CMK, per-env decrypt grants for pipeline roles only, no human decrypt.
- **Plan artifacts** are redacted (sensitive values and `*_password`, `*_secret`, `*_token` attributes replaced) before upload to `iacp-plan-artifacts-*` and PR comments. The plan handed to the Infracost job (`cost-input`) is built from an allow-list of the fields Infracost reads (resource addresses, types, providers, actions, non-sensitive planned values, provider region and default tags, reference-only expressions), not from a deny-list: new or unknown plan keys, `resource_drift`, variable defaults and literal configuration expressions never leave the validate job.
- **Shared internal token** (`IACP_INTERNAL_TOKEN`): the API does not scope it to a workflow (route-class and provenance checks are skipped for it). It is never given to a job that runs pull-request code, and a job with `id-token: write` never falls back to it. Where a workflow needs it, it lives in a GitHub Environment limited to `main`. GitHub Actions use OIDC (docs/05 section 4.1).
- **Validate reports are PR-controlled.** The pr-validate `report` job runs the PR's own code on a summary that the PR's Terraform produced. OIDC limits it to `/v1/runs` for `validate` on its own merge commit, but the backend must never treat a `validate` `succeeded` as an approval on its own. The approval is the review plus the plan hash; `apply.yml` re-plans and re-runs the policies.
- **Logs:** `slog` redaction handler for known secret keys; Actions `::add-mask::` for any dynamically fetched value.
- **Detection:** GitHub secret scanning + push protection on the repo; `gitleaks` as a required PR check.

## 5. Supply chain

| Control | Implementation |
|---|---|
| Actions pinned by SHA | Every `uses:` references a full 40-char commit SHA with a version comment, e.g. `actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2`, `aws-actions/configure-aws-credentials@e3dd6a429d7300a6a4c196c26e071d42e0343502 # v4.0.2`. Org policy: "Allow select actions" allowlist; `pin-github-action` check in CI fails on tag refs. Dependabot updates SHAs via PR. |
| Workflow integrity | `.github/CODEOWNERS` is generated from `.github/teams.yaml` by `python -m tools.codeowners` (team stack roots `environments/<env>/<team>/` → `@acme/<team>`, per-environment overrides such as production → `@acme/<team>-maintainers`; platform owners on `modules/`, `policies/`, `.github/`); CI runs `--check` and fails on drift or on a stack whose team is not declared. The branch ruleset and Environment protection payloads live in `.github/rulesets/` and are applied by the repo owner with `gh api` (WBS 2.5). `.github/**` CODEOWNERS = `@acme/platform-security`; `permissions: {}` at top, granted per job; `id-token: write` only on AWS/callback jobs; `pull_request` (not `pull_request_target`) for untrusted code. `security-events: write` (SARIF upload, WBS 6.1) exists only on the `code-scanning` jobs of `ci.yml` and `pr-validate.yml`. They run checkout and the SHA-pinned `github/codeql-action/upload-sarif` and nothing from the PR: the scanning jobs, which do run PR code, hand the SARIF over as an artifact. That SARIF is PR-produced: `tools.stack_pipeline sarif` drops each run's `automationDetails` and keeps every path inside the repository, so a PR cannot choose its category. Its findings only affect the PR's own analysis. Fork and Dependabot PRs skip the upload. |
| Module ref pinning | Generated `source` always `?ref=modules/<name>/vX.Y.Z`; tag protection prevents move/delete; `iacp.stack.json` stores tag→SHA; `apply.yml` runs `git rev-parse` and fails on mismatch. OPA denies non-tag refs and non-`acme/self-service-iac-platform` sources. |
| Provider lockfile | Each generated stack commits `.terraform.lock.hcl` produced by `terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_arm64`; CI runs `terraform init -lockfile=readonly`. Provider versions constrained by `module.yaml` ranges. |
| Provider mirror | Optional: `provider_installation { network_mirror { url = "https://tf-mirror.iacp.acme.internal/" } }` backed by S3 for availability and vetting. |
| Tool versions | `terraform`, `tflint`, `checkov`, `conftest`, `infracost` versions pinned in `.tool-versions`; downloaded binaries verified by SHA-256 in the workflow. Today: `.terraform-version`, workflow `env` (`TFLINT_VERSION`, `CONFTEST_VERSION`/`CONFTEST_SHA256`, `INFRACOST_VERSION`/`INFRACOST_SHA256`, `ACTIONLINT_VERSION`/`ACTIONLINT_SHA256`) and `policies/checkov/requirements.txt`. |
| Container images | Backend/portal images built in Actions, SBOM (Syft) + vulnerability scan (Grype, fail on critical), signed with cosign keyless (Sigstore, OIDC identity of `release.yml@main`); ECS pulls by digest; ECR immutable tags. |
| Module release provenance | `module-release.yml` produces SLSA provenance attestation (`actions/attest-build-provenance`) over the module tarball; catalog records attestation URL. |
| Dependencies | Go modules via `go.sum` + `govulncheck`; npm via lockfile + `npm audit --omit=dev` threshold high; Dependabot weekly. |

## 6. Policy as code

### 6.1 Layout and evaluation

```
policies/
├── opa/
│   ├── lib/iacp.rego                  # lib.iacp: pure helpers (selection, tags, environment, exceptions, messages)
│   ├── main.rego                      # main: aggregates every iacp.* package for `conftest test -p policies/opa`
│   ├── tags.rego                      # iacp.tags        (required iacp:* tags; iacp.tags.mismatch vs context)
│   ├── encryption.rego                # iacp.encryption  (S3 SSE-KMS, SQS and SNS SSE, RDS storage, ElastiCache at rest + TLS; CMK outside dev, also for DynamoDB; ECR KMS outside dev; KMS rotation)
│   ├── s3_public.rego                 # iacp.s3          (public access block, public ACLs, public bucket policies)
│   ├── network.rego                   # iacp.network     (publicly accessible RDS instances, ECS tasks with public IPs)
│   ├── ecr.rego                       # iacp.ecr         (scan on push, immutable tags)
│   ├── iam.rego                       # iacp.iam         (Action "*", NotAction/NotResource, write or sensitive actions on all resources, admin managed policies)
│   ├── placement.rego                 # iacp.placement   (allowed regions; instance sizes are planned)
│   ├── forbidden.rego                 # iacp.forbidden   (pinned module source, force_destroy outside development, data external/http, provisioners)
│   ├── modules.rego                   # iacp.modules     (module calls pinned to a yanked version, from the API context)
│   ├── budget.rego                    # iacp.budget      (team budget: context.budget + the Infracost estimate in context.estimated_monthly_cost_usd)
│   ├── prod_approval.rego             # iacp.approval    (planned; pre-apply gate)
│   └── *_test.rego                    # opa test cases; CI gate: coverage >= 90 %
├── example-plan.sh                    # plans a module's examples/complete and runs conftest with its policy-context*.json
├── fixtures/                          # sample plan JSONs, context.json / context-yanked.json + verify.sh (outside opa/: opa test loads JSON as data)
└── checkov/
    ├── __init__.py                    # makes the dir loadable by --external-checks-dir policies/checkov
    ├── iacp_aws_001_required_tags.py  # IACP_AWS_001
    ├── iacp_aws_002_s3_sse_kms.py     # IACP_AWS_002
    ├── iacp_aws_003_lambda_tracing_concurrency.py  # IACP_AWS_003
    └── tests/                         # pytest suite (no __init__.py, so Checkov never loads it)
```

The pipeline must always pass the stack context (`--data context.json`). Without it the policies fall back to safe defaults: allowed regions `us-east-1`/`us-west-2`, module sources from `github.com/bibekgupta3333/self-service-iac-platform` (override with `allowed_module_repos`), no ownership-tag comparison, and environment `unknown`. A resource's own `iacp:environment` tag is never trusted to relax a rule, so `unknown` is treated as non-development and `force_destroy` and SQS-managed SSE stay denied (fail closed). Every message has the form `[rule.id] <address>: <problem>. Fix: <remedy>`.

Bucket controls (SSE, public access block, policy, ACL) are linked to a bucket through the `bucket` expression's configuration references (or equal names known at plan time); a control that cannot be linked does not count. Every bucket that will exist is evaluated, so weakening or deleting a control on an unchanged bucket is caught. Policy documents computed at apply time are read from a referenced `data.aws_iam_policy_document` (its actions and principals stay known when resources are computed); a document that still cannot be read produces an `iacp.iam.policy_unknown` / `iacp.s3.policy_unknown` warning for the reviewer.

`pr-validate.yml` evaluation:

```bash
terraform show -json tfplan > plan.json
curl -sf -H "Authorization: Bearer $OIDC" "$IACP_API/v1/internal/budget-context?stack_path=$STACK" > context.json
# Default namespace `main` aggregates every iacp.* plan package (tags, encryption, s3,
# iam, placement, forbidden), so no rule can be left out by a missing --namespace.
conftest test plan.json      --policy policies/opa --data context.json --output json > opa-plan.json
# The budget gate is its own job (`budget` in pr-validate.yml, see 6.7): it runs after the
# cost job, because the estimate does not exist when the plan is evaluated.
```

`context.json` (served by the API) — exposed in Rego as `data.iacp.context`:

```json
{
  "iacp": {
    "context": {
      "environment": "production",
      "team": "payments",
      "stack": "order-events",
      "allowed_regions": ["us-east-1", "us-west-2"],
      "budget": { "month": "2026-11", "limit_usd": 2000.0, "spent_usd": 1450.10, "actual_usd": null, "hard_block": true },
      "budget_override": { "granted_by": "ada", "reason": "launch week capacity", "expires_at": "2026-12-01T00:00:00Z", "max_delta_usd": 750 },
      "current_committed_usd": 1450.10,
      "estimated_monthly_cost_usd": 120.50,
      "budget_gate": true,
      "yanked_module_versions": [ { "module": "s3", "version": "1.1.0", "tag": "modules/s3/v1.1.0" } ],
      "exceptions": [
        { "rule_id": "iacp.encryption.sqs_kms", "resource_address": "module.legacy_queue.aws_sqs_queue.this",
          "expires_at": "2026-12-31T00:00:00Z" }
      ],
      "now": "2026-11-24T10:05:00Z"
    }
  }
}
```

The API serves this document from `GET /v1/internal/budget-context` ([03 §6.9](03-data-model-and-api.md#69-pipeline-runs-actions-callbacks)) to `pr-validate.yml` and `apply.yml` (route class `budget_context`). The validate job still builds a reduced context from `iacp.stack.json` with `tools.stack_pipeline context`, which has no budget and no yanked versions; the budget job builds its own with `context --budget-gate` (6.7). `estimated_monthly_cost_usd` and `budget_gate` appear only in the budget job's context, and only `tools.stack_pipeline` sets them: the API never does.

**Accepted exposure.** The `budget_context` route class is not bound to the stacks a pull request changes: a `pr-validate.yml` token (whose workflow file the PR author controls) can read the context of any `stack_path`, and so any team's monthly budget, spend estimate, allowed regions and the yanked-version list. The data is read-only and low sensitivity, and nothing is written. Since WBS 8.5 the document also carries the team's `actual_usd`, the committed cost of its other stacks and any budget override (the admin's login, the reason text, the expiry and the bound), so the reason must not hold anything sensitive. Binding `stack_path` to the PR's changed stacks (from the webhook-recorded PR files) is the planned tightening if budgets become sensitive.

Rules emit `deny` (blocking) and `warn` (comment only). Each message is prefixed by a stable rule ID used by exceptions and metrics.

### 6.2 Shared library

```rego
# policies/opa/lib/iacp.rego
package lib.iacp

import rego.v1

ctx := data.iacp.context

# Resources being created or updated (after-state present).
changed contains rc if {
	some rc in input.resource_changes
	rc.mode == "managed"
	some action in rc.change.actions
	action in {"create", "update"}
}

changed_of_type(t) := {rc | some rc in changed; rc.type == t}

deleting(rc) if "delete" in rc.change.actions

exempt(rule_id, address) if {
	some e in ctx.exceptions
	e.rule_id == rule_id
	e.resource_address in {address, null}
	time.parse_rfc3339_ns(e.expires_at) > time.parse_rfc3339_ns(ctx.now)
}

# All resources in the planned configuration addressed by type, for cross-resource checks.
planned_of_type(t) := {r | some r in input.planned_values.root_module.child_modules[_].resources; r.type == t}

msg(rule_id, address, text) := sprintf("[%s] %s: %s", [rule_id, address, text])
```

### 6.3 Required tags

```rego
# policies/opa/tags.rego
package iacp.tags

import rego.v1
import data.lib.iacp as f

required := {"iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by"}

# Resource types that do not support tags are skipped. (aws_lambda_event_source_mapping
# is taggable since AWS provider 5.70; the lambda module tags it and requires >= 5.70.)
untaggable := {"aws_iam_role_policy", "aws_iam_role_policy_attachment",
	"aws_s3_bucket_public_access_block", "aws_s3_bucket_versioning",
	"aws_s3_bucket_server_side_encryption_configuration", "aws_s3_bucket_policy",
	"aws_s3_bucket_lifecycle_configuration", "aws_sqs_queue_policy", "aws_sqs_queue_redrive_policy",
	"aws_kms_alias", "aws_lambda_permission", "random_id", "random_password"}

deny contains m if {
	some rc in f.changed
	not rc.type in untaggable
	tags := object.get(rc.change.after, "tags_all", {})
	missing := required - {k | some k, _ in tags}
	count(missing) > 0
	not f.exempt("iacp.tags.required", rc.address)
	m := f.msg("iacp.tags.required", rc.address, sprintf("missing tags %v", [sort(missing)]))
}

deny contains m if {
	some rc in f.changed
	tags := object.get(rc.change.after, "tags_all", {})
	tags["iacp:team"] != f.ctx.team
	m := f.msg("iacp.tags.mismatch", rc.address,
		sprintf("iacp:team=%q does not match owning team %q", [tags["iacp:team"], f.ctx.team]))
}
```

### 6.4 Encryption required

Implemented in `policies/opa/encryption.rego`. "Customer-managed" means a key ID, ARN or alias that is not an AWS-managed `alias/aws/*` key, or a key created in the same plan. Outside development includes plans without a stack context (fail closed).

| Rule ID | Denies |
|---------|--------|
| `iacp.encryption.s3_kms` | a bucket (new or unchanged) without an SSE-KMS default encryption rule |
| `iacp.encryption.s3_cmk` | outside development, a bucket whose SSE-KMS rule uses the AWS-managed `aws/s3` key |
| `iacp.encryption.sqs_sse` | a queue with neither `kms_master_key_id` nor SQS-managed SSE |
| `iacp.encryption.sqs_kms` | outside development, a queue without a customer-managed key |
| `iacp.encryption.sns_sse` | a topic without `kms_master_key_id` |
| `iacp.encryption.sns_kms` | outside development, a topic on the AWS-managed `aws/sns` key (CloudWatch alarms cannot publish to it) |
| `iacp.encryption.dynamodb_cmk` | outside development, a table on the AWS owned or AWS-managed `aws/dynamodb` key. The provider computes `kms_key_arn` when it is not set, so a computed key only counts when the configuration sets `kms_key_arn` |
| `iacp.encryption.rds_storage` | an RDS instance or cluster without `storage_encrypted` |
| `iacp.encryption.rds_cmk` | outside development, RDS storage without a customer-managed `kms_key_id` |
| `iacp.encryption.elasticache_at_rest` / `elasticache_transit` | an ElastiCache replication group without at-rest encryption / without TLS in transit |
| `iacp.encryption.elasticache_cmk` | outside development, an ElastiCache replication group without a customer-managed `kms_key_id` |
| `iacp.encryption.ecr_kms` | outside development, an ECR repository without KMS and a customer-managed `kms_key` |
| `iacp.encryption.kms_rotation` | a KMS key with rotation off |

The original design sketch follows; the rule IDs above are authoritative.

```rego
# policies/opa/encryption.rego
package iacp.encryption

import rego.v1
import data.lib.iacp as f

# S3: every bucket must have an SSE configuration using aws:kms.
deny contains m if {
	some b in f.changed_of_type("aws_s3_bucket")
	not bucket_has_kms(b)
	not f.exempt("iacp.encryption.s3_kms", b.address)
	m := f.msg("iacp.encryption.s3_kms", b.address, "bucket must use SSE-KMS (aws_s3_bucket_server_side_encryption_configuration with aws:kms)")
}

bucket_has_kms(b) if {
	some sse in f.planned_of_type("aws_s3_bucket_server_side_encryption_configuration")
	startswith(sse.address, module_prefix(b.address))
	some rule in sse.values.rule
	some d in rule.apply_server_side_encryption_by_default
	d.sse_algorithm in {"aws:kms", "aws:kms:dsse"}
}

module_prefix(addr) := concat(".", array.slice(split(addr, "."), 0, 2))   # module.<name>

# SQS: customer-managed KMS required outside development; SQS-managed SSE minimum in development.
deny contains m if {
	some q in f.changed_of_type("aws_sqs_queue")
	f.ctx.environment != "development"
	not q.change.after.kms_master_key_id
	not f.exempt("iacp.encryption.sqs_kms", q.address)
	m := f.msg("iacp.encryption.sqs_kms", q.address, "queue must set kms_master_key_id in staging/production")
}

deny contains m if {
	some q in f.changed_of_type("aws_sqs_queue")
	not q.change.after.kms_master_key_id
	q.change.after.sqs_managed_sse_enabled != true
	m := f.msg("iacp.encryption.sqs_any", q.address, "queue must be encrypted")
}

# RDS / ElastiCache / EBS
deny contains m if {
	some r in f.changed_of_type("aws_db_instance")
	r.change.after.storage_encrypted != true
	m := f.msg("iacp.encryption.rds", r.address, "storage_encrypted must be true")
}

deny contains m if {
	some r in f.changed_of_type("aws_elasticache_replication_group")
	not all_true([r.change.after.at_rest_encryption_enabled, r.change.after.transit_encryption_enabled])
	m := f.msg("iacp.encryption.elasticache", r.address, "at-rest and in-transit encryption required")
}

all_true(xs) if every x in xs { x == true }

# KMS keys must rotate.
deny contains m if {
	some k in f.changed_of_type("aws_kms_key")
	k.change.after.enable_key_rotation != true
	m := f.msg("iacp.encryption.kms_rotation", k.address, "enable_key_rotation must be true")
}
```

### 6.5 No public S3

```rego
# policies/opa/s3_public.rego
package iacp.s3

import rego.v1
import data.lib.iacp as f

# Every bucket needs a public access block with all four settings true.
deny contains m if {
	some b in f.changed_of_type("aws_s3_bucket")
	not has_full_pab(b)
	not f.exempt("iacp.s3.no_public", b.address)
	m := f.msg("iacp.s3.no_public", b.address, "aws_s3_bucket_public_access_block with all four flags = true is required")
}

has_full_pab(b) if {
	some p in f.planned_of_type("aws_s3_bucket_public_access_block")
	startswith(p.address, f.module_prefix(b.address))
	p.values.block_public_acls
	p.values.block_public_policy
	p.values.ignore_public_acls
	p.values.restrict_public_buckets
}

# No public ACLs.
deny contains m if {
	some a in f.changed_of_type("aws_s3_bucket_acl")
	a.change.after.acl in {"public-read", "public-read-write", "authenticated-read"}
	m := f.msg("iacp.s3.no_public_acl", a.address, sprintf("ACL %q is not allowed", [a.change.after.acl]))
}

# Bucket policies must not grant to "*" without an org or source condition.
deny contains m if {
	some p in f.changed_of_type("aws_s3_bucket_policy")
	doc := json.unmarshal(p.change.after.policy)
	some s in doc.Statement
	s.Effect == "Allow"
	wildcard_principal(s.Principal)
	not restricted(s)
	m := f.msg("iacp.s3.no_public_policy", p.address, "Allow to Principal \"*\" requires aws:PrincipalOrgID or aws:SourceArn condition")
}

wildcard_principal("*")
wildcard_principal(p) if p.AWS == "*"
wildcard_principal(p) if "*" in p.AWS

restricted(s) if {
	some op, conds in s.Condition
	some k, _ in conds
	lower(k) in {"aws:principalorgid", "aws:sourcearn", "aws:sourceaccount", "aws:sourcevpce"}
}
```

### 6.6 Allowed regions and instance types

```rego
# policies/opa/regions_and_sizes.rego
package iacp.placement

import rego.v1
import data.lib.iacp as f

# Provider region (constant in generated providers.tf).
deny contains m if {
	some name, p in input.configuration.provider_config
	p.name == "aws"
	region := p.expressions.region.constant_value
	not region in f.ctx.allowed_regions
	m := f.msg("iacp.placement.region", sprintf("provider.%s", [name]),
		sprintf("region %q not in allowed %v", [region, f.ctx.allowed_regions]))
}

allowed_sizes := {
	"development": {
		"aws_db_instance": {"db.t4g.micro", "db.t4g.small", "db.t4g.medium"},
		"aws_elasticache_replication_group": {"cache.t4g.micro", "cache.t4g.small"},
		"aws_instance": {"t4g.micro", "t4g.small", "t4g.medium"},
	},
	"staging": {
		"aws_db_instance": {"db.t4g.medium", "db.t4g.large", "db.r7g.large"},
		"aws_elasticache_replication_group": {"cache.t4g.small", "cache.t4g.medium", "cache.r7g.large"},
		"aws_instance": {"t4g.medium", "t4g.large", "m7g.large"},
	},
	"production": {
		"aws_db_instance": {"db.r7g.large", "db.r7g.xlarge", "db.r7g.2xlarge"},
		"aws_elasticache_replication_group": {"cache.r7g.large", "cache.r7g.xlarge"},
		"aws_instance": {"m7g.large", "m7g.xlarge", "c7g.xlarge"},
	},
}

size_attr := {"aws_db_instance": "instance_class", "aws_elasticache_replication_group": "node_type", "aws_instance": "instance_type"}

deny contains m if {
	some rc in f.changed
	attr := size_attr[rc.type]
	size := rc.change.after[attr]
	not size in allowed_sizes[f.ctx.environment][rc.type]
	not f.exempt("iacp.placement.size", rc.address)
	m := f.msg("iacp.placement.size", rc.address,
		sprintf("%s %q not allowed in %s", [attr, size, f.ctx.environment]))
}

# Lambda memory ceiling (cost guard).
deny contains m if {
	some rc in f.changed_of_type("aws_lambda_function")
	rc.change.after.memory_size > 3008
	m := f.msg("iacp.placement.lambda_memory", rc.address, "memory_size above 3008 MB requires an exception")
}
```

### 6.7 Budget limit

`policies/opa/budget.rego` (`iacp.budget`) denies a change whose estimated monthly cost would push the team's month over a hard budget, denies a single stack above a cap, and warns near the limit. It reads `data.iacp.context` only; the plan is input only to recognise a change that adds nothing. Decisions and deferrals: [ADR-0014](adr/0014-budget-gate-inputs-and-deferrals.md).

| Context key | Source | Meaning |
|---|---|---|
| `budget` | API (`GET /v1/internal/budget-context`) | `limit_usd`, `spent_usd` (committed estimates), `actual_usd` (recorded actual, may be null), `hard_block`, `month`, optional `single_change_cap_usd` (none unless set). Absent or null: no budget, nothing to enforce. An optional `warn_pct` overrides the 80 percent warning threshold. |
| `current_committed_usd` | API | The committed monthly cost of the team's *other* active stacks in the budget's scope (this stack excluded; one environment for an environment budget, all for a team-wide one). |
| `budget_override` | API | An admin override (`granted_by`, `reason`, `expires_at`, `max_delta_usd`), granted with `PUT /v1/teams/{slug}/budgets/{month}`. |
| `estimated_monthly_cost_usd` | `tools.stack_pipeline context --budget-gate`, from the isolated cost job's Infracost output | One plain number: the stack's whole monthly cost as planned (not a delta). Missing when Infracost did not run, reported no total, a negative total or a non-USD currency. |
| `budget_gate` | `tools.stack_pipeline` | `true` only in the budget job. Only then does a missing estimate count; the validate job evaluates every rule before any estimate exists. |

Already used is the largest of `spent_usd`, `actual_usd` and `current_committed_usd`; projected is used plus the estimate. Rules (every message names the stack and the fix):

| Rule | Level | When |
|---|---|---|
| `iacp.budget.exceeded` | deny | Estimate above zero, projected above the limit, the budget is a hard block (`hard_block` is anything but `false`) and no override is in effect. |
| `iacp.budget.exceeded_waived` | warn | The same, but the budget is advisory or an override is in effect. |
| `iacp.budget.single_change_cap` | deny | The budget sets a `single_change_cap_usd` and the estimate is above it, whatever the headroom, **even on an advisory budget** (`hard_block` false only turns the over-limit rule into a warning; the cap still denies), and no override covers the excess. There is no default cap: the estimate is the whole stack, not the change, so a default would block every update of an existing stack above it; an admin opts in per budget. |
| `iacp.budget.single_change_cap_waived` | warn | The same, with an override in effect. |
| `iacp.budget.near_limit` | warn | Projected is at or above 80 percent of the limit and not over it. |
| `iacp.budget.no_estimate` | deny / warn | A budget is set, the gate is on and there is no usable estimate. **Development: warn** (the estimate is informational there; a development budget holds only while Infracost succeeds). **Staging, production and an unknown environment: deny** while the budget is a hard block, otherwise warn. Cost data that cannot be read never reads as free, and an override cannot waive it. |
| `iacp.budget.invalid` | deny | The budget object has no numeric `limit_usd`. |

A plan that creates or updates nothing (destroy only, or no change) adds no cost and always passes, so a team over budget can free budget; a missing plan is not assumed to be one. **The override** waives a hard limit or the cap in development and staging only, until its `expires_at` (judged against the API's `now`), and only while the overrun it allows is at most its `max_delta_usd` (`projected - limit` for the limit, `estimate - cap` for the cap), so several stacks under one override cannot overrun by more than that each; without an estimate there is nothing to bound, so it never makes a missing estimate acceptable. It never waives anything in production, and an unknown environment counts as production: the denial message then says so. The API refuses an override on a production-scoped budget. Budget rules are not waivable through policy exceptions (they do not use `exempt()`). Usage-based resources are priced at zero usage by Infracost, so the estimate is a floor. With Infracost disabled (`vars.INFRACOST_ENABLED` not `true`), every staging and production change of a team that has a hard budget is denied (`no_estimate`): enable Infracost before setting such a budget.

**The limit is per stack until the committed cost is recorded.** `current_committed_usd` is read from `stacks.committed_monthly_usd`, written by `POST /v1/internal/budget-committed`. Nothing calls it yet (`apply.yml` does not price a plan; ADR-0014), and `spent_usd` is not filled either, so today `used` is 0 and each stack is compared alone with the team's whole limit: spend split over several stacks, each under the limit, is not caught. This is a known gap with a tracked follow-up (WBS 8.5), not a property of the design.

**Trust boundary.** `pr-validate.yml` runs the PR's own workflow, `tools/`, `policies/` and *stack root*, so a gate that took its inputs from them could be talked out of its answer. What is and is not guaranteed:

- **No PR program runs at plan time.** `terraform plan` runs code (`data "external"` starts a program, `data "http"` calls out, a provider is a binary), and the OPA `iacp.forbidden.*` rules only see those constructs in the plan JSON, after they ran. The `validate` job therefore runs `tools.stack_pipeline check-stack` from the **base commit** before `terraform init`: a stack root may contain only `terraform`, `provider`, `module`, `variable`, `output`, `locals` and `moved` blocks (so no `resource`, `data` or provisioner), providers only from `hashicorp/aws` and `hashicorp/random` (in code and in `.terraform.lock.hcl`), the `local` or `s3` backend, module sources only the platform repository at an exact `modules/<name>/vX.Y.Z` tag, no JSON configuration and no symlinks. The root may hold **only the files the generator writes** (plus the lock): any directory, so no committed `.terraform/providers/...` binary that `terraform init -lockfile=readonly` would reuse without a download or signature check, no `.terraform.d`, no dotfile, no extra file. The lock's **pins** must already exist on the base commit: every provider version and every quoted string in its `hashes` list must appear in a lock file of the base checkout, so a lock that names a provider package the PR made up is refused. Because Terraform decodes HCL string escapes and templates and a scanner does not, any backslash, `${`, `%{` or heredoc (`<<`) in the lock file is refused outright (`terraform init` never writes one), the lock is read with `#`, `//` and `/* */` comments stripped outside strings (a commented-out `hashes = [...]`, or a `]` in a comment, cannot steer the pin check), the `hashes` list is read to its real end with the string scanner, and a provider block with zero or several `version` or `hashes` attributes is refused: `"h1:...\u003d"` would otherwise read as a hash the pin check never saw. The Terraform plugin cache lives under `$RUNNER_TEMP`, not in the checkout, `rm -rf .terraform` precedes `init`, and `pr-validate.yml` restores no provider cache and no TFLint plugin cache at all, and keeps both plugin directories under `$RUNNER_TEMP`: caches saved on a PR's own ref are restorable by that PR's later runs, so a poisoned provider package or TFLint plugin binary saved by an earlier run (with a changed workflow) would otherwise sit where a later, clean-looking run's `init` or `tflint` finds it (TFLint runs before `terraform plan`, so it could rewrite the tools or the plan). `apply.yml` and `drift.yml` run on `main` and restore only with an exact key. (Not done: recomputing the `h1:` of each package under `.terraform/providers` after `init` and comparing it with the base allow-list.) The check runs under `python -I` from the base checkout, so nothing in the PR's tree can shadow it, and if the base has the guard and it fails the step fails (there is no fallback; only the one change that introduces the guard, which has no base copy, uses its own). Generated stacks satisfy it by construction. Without it a team-owned stack root could rewrite `cost.py` or `plan.json` at plan time and have the isolated cost job price a forged plan.
- The `budget` job checks out the **base commit**: the policy, the context builder and the parser it runs are the target branch's, whatever the PR changes in `policies/` or `tools/`.
- The estimate comes **only** from the `cost-<id>` artifact of this run, and only when the `cost` job succeeded: the output of the pinned, checksum-verified Infracost binary on the redacted plan, in the one job with `INFRACOST_API_KEY`. The budget job has no key and reads nothing from the PR's stack directory: identity is the detected stack path (`--stack-dir` is refused). Any `estimated_monthly_cost_usd` or `budget_gate` found elsewhere, including in the API's document, is discarded, and a total that is not a non-negative USD number becomes "missing", which is denied in staging and production.
- The budget, the override, the committed total and the clock come from the Platform API over OIDC; the document must name the same environment, team, stack and path or the job fails.
- **Residual risk, stated plainly.** The workflow file itself is the PR's version, so a PR that edits `.github/workflows/`, `tools/` or `policies/` could change the gate; those paths are platform-owned in CODEOWNERS and need a platform review, the boundary every gate in this workflow rests on. A hole in the static check would reopen the plan-time path. The artifact hand-off between jobs is only as strong as the workflow that uploads and downloads it. **The pre-apply gate that re-evaluates policy from `main` at apply time (WBS 8.8) is the only backstop** for these; until it exists, the `pr-validate` budget job is a strong early check, not the last word.
- With the API unconfigured (local-first, ADR-0009) there is no budget, nothing is enforced and the cost stays informational. With it configured, a failed budget fetch fails the job (an unreadable budget is not an absent one); a fork PR, which gets no OIDC token, therefore cannot pass the gate once budgets exist. `pr-validate-result` requires the budget job to succeed or be skipped.

**Actuals and committed cost.** `POST /v1/internal/budget-actuals` records `actual_usd` and `POST /v1/internal/budget-committed` records a stack's committed monthly cost. Both accept only the shared internal token (no OIDC workflow is in their route class, so a workflow token minted from a pull-request workflow file cannot call them). The token is a repository secret: a same-repo PR that edits a workflow can read it, which is the same known limit as above, so it should live in a GitHub Environment restricted to `main` before budgets are relied on. Pulling Cost Explorer by the `iacp:team` tag into `actual_usd` is not built: Cost Explorer is not available under LocalStack (ADR-0009).

### 6.8 Production requires approvals (pre-apply gate)

Branch protection + CODEOWNERS + the `production` GitHub Environment enforce approvals natively. As defence in depth (e.g. a misconfigured protection rule), `apply.yml` evaluates a pre-apply policy against the request record returned by `GET /v1/internal/requests/by-path` (with budget/exception context from `GET /v1/internal/budget-context`) (`approvals` populated from GitHub reviews and mapped to the platform roles):

```rego
# policies/opa/prod_approval.rego
package iacp.approval

import rego.v1

ctx := data.iacp.context

maintainer_approvals := {a.login |
	some a in input.request.approvals
	a.state == "APPROVED"
	a.role in {"maintainer", "admin"}
	a.login != input.request.author
	a.login != "iacp-approver"   # machine user never counts in production
}

destructive if {
	some rc in input.plan.resource_changes
	"delete" in rc.change.actions
}

deny contains "[iacp.approval.prod_min] production change needs >= 1 maintainer approval from someone other than the author" if {
	ctx.environment == "production"
	count(maintainer_approvals) < 1
}

deny contains "[iacp.approval.prod_destructive] production change with deletes/replacements needs >= 2 maintainer approvals" if {
	ctx.environment == "production"
	destructive
	count(maintainer_approvals) < 2
}

deny contains "[iacp.approval.plan_hash] plan differs from the approved plan" if {
	input.request.approved_plan_hash != input.plan_hash
}

# Stateful resources cannot be destroyed in production without explicit allow flag on the request.
stateful := {"aws_db_instance", "aws_rds_cluster", "aws_s3_bucket", "aws_elasticache_replication_group", "aws_dynamodb_table", "aws_kms_key"}

deny contains msg if {
	ctx.environment == "production"
	some rc in input.plan.resource_changes
	rc.type in stateful
	"delete" in rc.change.actions
	not input.request.allow_stateful_destroy
	msg := sprintf("[iacp.approval.stateful_destroy] %s would be deleted; request must set allow_stateful_destroy", [rc.address])
}
```

### 6.9 Forbidden constructs

Implemented in `policies/opa/forbidden.rego` (none of these can be waived):

| Rule ID | Denies |
|---------|--------|
| `iacp.forbidden.module_source` | a root module call whose source is not `git::https://github.com/<allowed repo>.git//modules/<name>?ref=modules/<name>/vX.Y.Z` (same `<name>` in path and ref; never a branch such as `main`) |
| `iacp.forbidden.force_destroy` | `force_destroy = true` on any resource unless the context environment is `development` |
| `iacp.forbidden.data_source` | `data "external"` and `data "http"` anywhere in the configuration, including nested modules (they run programs or reach arbitrary URLs during plan) |
| `iacp.forbidden.provisioner` | any resource with a provisioner (`local-exec`, `remote-exec`, `file`) |

The data-source and provisioner rules read `configuration` rather than `resource_changes`, so a data source that is fully known at plan time (and therefore absent from `resource_changes`) is still caught. Denying `null_resource` / `terraform_data` outright is planned but not enforced; such resources only become dangerous with a provisioner, which is denied.

#### Yanked module versions

Implemented in `policies/opa/modules.rego` from `data.iacp.context.yanked_module_versions`:

| Rule ID | Effect |
|---------|--------|
| `iacp.modules.yanked` (deny) | a root module call pinned (`?ref=modules/<name>/vX.Y.Z`) to a yanked version while the plan creates or updates resources inside that module (nested modules and instance keys included) |
| `iacp.modules.yanked_pinned` (warn) | the same pin when the plan leaves the module untouched or only destroys it: plans and destroys on a yanked version keep working ([06 §2](06-lifecycle-management.md)), but the stack should take the upgrade request |

Without the API context nothing is listed as yanked, so the rule is inert. The deny can be waived with a time-boxed exception for `module.<name>`.

Policy tests (`opa test policies/opa -v --coverage`) run in `ci.yml` on every PR and push to `main`; the coverage gate is 90 % (currently about 99.9 %). `policies/fixtures/verify.sh` runs conftest with a stack context against a compliant plan (no denies or warnings allowed) and a non-compliant plan that must trip every deny and warn rule ID, then reruns the compliant plan with `context-yanked.json` (its pinned `modules/s3/v1.2.0` listed as yanked), which must be denied by `iacp.modules.yanked` alone. `policies/example-plan.sh` plans each module's `examples/complete` (against LocalStack, which only answers STS while planning) and runs conftest with the example's `policy-context.json`, plus each `policy-context.<env>.json` (for example staging) against a plan made with `-var environment=<env>`; the only deny it accepts is `iacp.forbidden.module_source` for a source starting with `../`, because examples call their module through a relative path. A `?ref=main`, third-party or otherwise unpinned source still fails; `policies/fixtures/verify-example-gate.sh` proves this with the plans in `policies/fixtures/example-gate/`. CI runs it for every module in the `module-tests` job.

For data stores and containers, the attributes that matter are only known at plan time when the configuration sets them. RDS reports `kms_key_id` as computed even when it is omitted (AWS then uses `aws/rds`), so `iacp.encryption.rds_cmk` and `iacp.encryption.elasticache_cmk` accept an unknown key only when the configuration sets `kms_key_id`. Missing flags such as `storage_encrypted` or `transit_encryption_enabled` count as off. Two known edges: an expression that only resolves to an AWS-managed key at apply time is not caught, and an RDS read replica or snapshot restore that inherits encryption without setting `storage_encrypted` is denied by `iacp.encryption.rds_storage` and needs an exception. `iacp.encryption.ecr_kms` treats `encryption_type = "KMS"` without a `kms_key` as the AWS-managed `aws/ecr` key.

### 6.10 Custom Checkov checks

Checkov runs on the generated root module **and** on each module's `examples/` during release (`checkov -d . --external-checks-dir policies/checkov --download-external-modules true --framework terraform`).

Implemented custom checks (class-based `BaseResourceCheck`, unit tested in `policies/checkov/tests/`):

| ID | Resource | Rule |
|----|----------|------|
| `IACP_AWS_001` | taggable AWS resources | `tags` sets `iacp:team`, `iacp:environment`, `iacp:managed-by` (`iacp:stack` comes from provider `default_tags` and is checked by OPA) |
| `IACP_AWS_002` | `aws_s3_bucket_server_side_encryption_configuration` | `sse_algorithm` is `aws:kms` or `aws:kms:dsse` with `kms_master_key_id` set |
| `IACP_AWS_003` | `aws_lambda_function` | `tracing_config.mode = "Active"` and `reserved_concurrent_executions >= 0` |

The `CKV_FDY_*` examples below are design sketches for further checks.

Python check — SQS queues must have a dead-letter queue:

```python
# policies/checkov/checks/sqs_redrive.py
from checkov.common.models.enums import CheckCategories, CheckResult
from checkov.terraform.checks.resource.base_resource_check import BaseResourceCheck


class SQSQueueHasRedrivePolicy(BaseResourceCheck):
    def __init__(self) -> None:
        super().__init__(
            name="Ensure SQS queues (other than DLQs) configure a redrive policy",
            id="CKV_FDY_001",
            categories=[CheckCategories.GENERAL_SECURITY],
            supported_resources=["aws_sqs_queue"],
        )

    def scan_resource_conf(self, conf: dict) -> CheckResult:
        tags = (conf.get("tags") or [{}])[0] or {}
        if tags.get("iacp:role") == "dlq":
            return CheckResult.PASSED
        redrive = conf.get("redrive_policy")
        if redrive and redrive[0]:
            return CheckResult.PASSED
        # Redrive may be configured via the separate resource; Checkov graph check below covers that.
        self.details.append("Queue has no redrive_policy; add a DLQ or tag iacp:role=dlq")
        return CheckResult.FAILED


check = SQSQueueHasRedrivePolicy()
```

YAML graph check — Lambda functions must enable X-Ray tracing and be attached to a role carrying the platform boundary:

```yaml
# policies/checkov/checks/lambda_tracing.yaml
metadata:
  id: "CKV2_FDY_002"
  name: "Ensure Lambda functions enable active tracing and use a platform-bounded role"
  category: "LOGGING"
definition:
  and:
    - cond_type: attribute
      resource_types: ["aws_lambda_function"]
      attribute: tracing_config.mode
      operator: equals
      value: Active
    - cond_type: connection
      resource_types: ["aws_lambda_function"]
      connected_resource_types: ["aws_iam_role"]
      operator: exists
    - cond_type: attribute
      resource_types: ["aws_iam_role"]
      attribute: permissions_boundary
      operator: contains
      value: "policy/iacp-boundary"
```

`.checkov.yaml` sets `soft-fail-on: LOW`, `hard-fail-on: MEDIUM,HIGH,CRITICAL`, and skips checks that duplicate OPA rules (to keep one source of truth per control). Inline `#checkov:skip` comments are **not** honoured in `environments/**` (generated code); waivers go through the exception workflow.

## 7. RBAC matrix

Roles are per team, derived from GitHub team membership: `<team>-viewers` → viewer, `<team>` (member) → developer, `<team>-maintainers` → maintainer. `admin` = member of `@acme/self-service-iac-platform-admins` (platform-wide). `security` approvers = `@acme/security` (orthogonal capability).

| Action | viewer | developer | maintainer | admin | security |
|---|:-:|:-:|:-:|:-:|:-:|
| Browse catalog, module docs | ✓ | ✓ | ✓ | ✓ | ✓ |
| View team stacks, requests, plans, costs, drift | ✓ | ✓ | ✓ | ✓ | ✓ (all teams) |
| Create request (create/update/upgrade) — development, staging | | ✓ | ✓ | ✓ | |
| Create request — production | | ✓ | ✓ | ✓ | |
| Create destroy request — development, staging | | ✓ | ✓ | ✓ | |
| Create destroy request — production | | | ✓ | ✓ | |
| Approve PR (CODEOWNERS) — non-prod | | ✓ (not own) | ✓ | ✓ | |
| Auto-approve TTL-expiry destroy PR — non-prod only | `iacp-approver` machine user only (system actor), audited as `request.auto_approved`; never in production | | | | |
| Approve PR / Environment — production | | | ✓ (not own) | ✓ | |
| Cancel own request | | ✓ | ✓ | ✓ | |
| Cancel others' request | | | ✓ | ✓ | |
| Retry failed apply / rollback (`POST /v1/stacks/{id}/rollback`) | | | ✓ | ✓ | |
| Break-glass state surgery (`state-surgery.yml`) | GitHub-side only: dispatch from main by the platform team, approval by a `break-glass` Environment reviewer (not the dispatcher); recorded as `state.surgery` | | | | |
| Extend TTL (within env max) | | ✓ | ✓ | ✓ | |
| Resolve drift: reconcile / codify | | ✓ | ✓ | ✓ | |
| Resolve drift: ignore | | | ✓ | ✓ | |
| Request policy exception | | | ✓ | ✓ | |
| Approve policy exception | | | | | ✓ |
| Set team budget | | | | ✓ | |
| Grant budget override | | | | ✓ | |
| View audit log (own team) | | | ✓ | ✓ | ✓ |
| View audit log (all), verify chain, export | | | | ✓ | ✓ |
| Deprecate module version | | | module owners | ✓ | |
| Force-release stack lock | | | | ✓ | |
| Manage environments / allowed regions | | | | ✓ | |

**Automated approvals.** GitHub Apps cannot be code owners, so the platform GitHub App never approves PRs. Automated approvals exist only in non-prod — notably **TTL-expiry destroy PRs** (and development auto-approval rules described in [05-automation-pipeline.md](05-automation-pipeline.md)) — and are submitted by a separate machine user `iacp-approver` (member of `@acme/self-service-iac-platform-approvers`, CODEOWNER only for `environments/development/**` and `environments/staging/**`; token in Secrets Manager, readable only by the worker task role; every use audited). Production is never auto-approved: `environments/production/**` CODEOWNERS and the `production` Environment reviewers exclude `@acme/self-service-iac-platform-approvers`, and the pre-apply OPA gate (§6.8) ignores approvals from `iacp-approver`.

**Implementation (WBS 4.4, 8.6 core).** The matrix is the pure table `backend/internal/rbac` (`rbac.Decide(role, action, resource)`), tested exhaustively against a hand-written grid of every role x action x environment. Unknown actions, unknown roles and unknown environments are denied (an unknown environment counts as production). Approving your own work is denied below admin. Two layers apply it: route policies (`httpapi.DefaultPolicies`: `GET /v1/audit/verify` needs a platform admin; `GET /v1/requests` and `/v1/requests/{id}` need a role in at least one team) and the services, which call the matrix with the caller's role in the target team (request creation). A user route without a policy is refused with 403 (fail closed), and `TestEveryUserRouteHasAPolicy` walks the router so a new route fails the build until it has an entry; only probes, the public login routes, machine routes (their own authenticator, never a session) and webhooks are exempt. Every 403 returned to an authenticated user is appended to `audit_events` as `rbac.denied` (route pattern, actor, IP, request id). In `dev` auth mode the fixed local user is a platform admin for route policies only; request creation still reads its team role from `memberships`. Production gates beyond the role thresholds (Environment reviewers, the pre-apply OPA check, two-person rules, re-auth for production destroy) belong to WBS 8.8. Where this table and the task wording "maintainer: destroy in non-prod" differ, this table wins: developers may create non-production destroy requests.

**GitHub login and team sync.** Sign-in is GitHub OAuth (authorization code with `state` and S256 PKCE) and requires an active membership of `IACP_GITHUB_ORG`, otherwise the login is denied and audited as `auth.login_denied`. On every login the user is upserted by GitHub id and memberships are synced from the user's GitHub team slugs (the access token is discarded, never stored). With `<slug>` a platform team's `github_team_slug`:

| GitHub team slug | Platform effect |
|---|---|
| `iacp-<slug>-viewers` | `viewer` in that team |
| `<slug>` | `developer` |
| `<slug>-maintainers` | `maintainer` |
| `IACP_AUTH_ADMIN_TEAM` (default `iacp-admins`) | `users.is_platform_admin` |

The highest matching role wins. Rows with `source = 'manual'` are never changed or removed by sync; rows it created (`github_sync`) are removed when the user leaves the GitHub team. Teams with no platform row are ignored. Sync never removes the last active platform admin: the downgrade is skipped and audited as `rbac.admin_downgrade_blocked` (serialised with an advisory lock, so two concurrent syncs cannot both succeed). Changes are audited as `rbac.membership_changed` and `rbac.platform_admin_changed`. Roles refresh at login and are bounded by the session lifetime (8 h by default); a periodic or webhook-driven sync needs an org-level credential (GitHub App, WBS 4.8) and is not built.

**Trust in GitHub team slugs.** Authority comes from team *names*, so whoever can create a GitHub team can mint roles. Before go-live: (1) the `IACP_AUTH_ADMIN_TEAM` team (`iacp-admins`) and every `<slug>-maintainers` and `iacp-<slug>-viewers` team you rely on must already exist, so nobody else can create them first; (2) restrict team creation (and team renaming) to organisation owners (Organization settings, Member privileges, "Team creation rules"), and keep membership of `iacp-admins` and the maintainer teams under owner or security control. The platform adds a guard but not a substitute: teams whose derived slugs overlap another team's or the admin team's (for example platform teams `payments` and `payments-maintainers`, or a team named `iacp-admins`) are detected (`teamsync.Collisions`), logged at startup, and receive no synced roles until fixed. Only teams in the configured organisation are read.

**Stale authority.** Roles and the admin flag are refreshed only at login, so a person removed from a GitHub team keeps their platform role for up to the session lifetime (8 hours by default; 1 hour idle). Disabling a user (`users.disabled_at`) ends their sessions immediately. Shorten `IACP_AUTH_SESSION_TTL` where that window is too long; a periodic or webhook-driven resync (needs an org credential, WBS 4.8) is the real fix and is not built.

**Read scoping.** `GET /v1/requests` and `GET /v1/requests/{id}` only return requests of the caller's teams (platform admins see all); a request in another team is `404`, identical to one that does not exist, and `?team=` can only narrow within the caller's teams. Services and SQL share one helper, `identity.Visibility` (`UserStore.VisibleTeams`, `Visibility.SQLArgs`, predicate `($all OR team_id = ANY($ids))`), which other read routes (stacks, events) use too. The audit routes are admin-only, and the catalog is shared reference data.

**Recovery (WBS 8.7).** `POST /v1/stacks/{id}/rollback` has the route policy `retry` (maintainer or higher in some team) and the service checks `rbac.Decide(role, retry, environment)` with the caller's role in the stack's team; no role there is `404`. The rollback is an ordinary update request, so creation, approval and production gates apply to it unchanged. Apply error messages are reduced to redacted `Error:` lines and fixed-shape signals before they are sent and again before they are stored (`tferror`, shared with drift). Automatic retries are system actions (actor `system/recovery`), bounded by the state machine's retry cap, and only re-run an approved, merged change on main: the retried run re-plans and must match the approved plan hash. The retry's `workflow_dispatch` is never re-sent after an error. Break-glass state surgery is gated outside the API (dispatch from main, `break-glass` Environment approval with prevent self-review); its audit report can only come from `state-surgery.yml` on main via `workflow_dispatch` with a GitHub OIDC token (route class `state_surgery`, OIDC only: the shared token is refused) for the token's own run, a run that did anything must carry the `break-glass` environment claim, the requester is the token's verified `actor`, its inputs are allow-listed and pattern-checked twice (before and after approval), never placed in a `run:` expression, never echoed raw into the log, and Terraform runs as an argv list with workflow commands paused.

**Abuse limits.** `GET /v1/auth/login` is limited to 30 per minute per remote address (`429 rate_limited`, `Retry-After`); behind the portal proxy the address is the portal's, so it acts as a global cap on login starts. `rbac.denied` audit events are limited to 20 per user per minute, and the next audited denial carries `suppressed_before`. Both limits are per process.

**Cookies.** Over https the session and login-state cookies are `__Host-iacp_session` and `__Host-iacp_oauth_state` (Secure, Path=/, no Domain, so a subdomain cannot overwrite them); an unprefixed look-alike is ignored. Plain-http loopback keeps the unprefixed names, because browsers refuse `__Host-` without Secure. `POST /v1/auth/logout` is public: with a live session it needs the CSRF token and audits `auth.logout`; with a missing or dead session it just clears the cookie and answers `204`. A login whose `auth.login` audit event cannot be written fails and its new session is revoked.

**OAuth app settings (repo owner, WBS 2.5).** Register an OAuth App (or GitHub App user authorization) owned by the organisation: Homepage URL = the portal origin; Authorization callback URL = `https://<portal>/api/iacp/v1/auth/callback` (the login runs through the portal's same-origin proxy so the session cookie belongs to the portal's origin; locally `http://localhost:3000/api/iacp/v1/auth/callback`). The backend requests the scopes `read:user user:email read:org`; an organisation that restricts third-party OAuth apps must approve it. Set `IACP_GITHUB_CLIENT_ID`, `IACP_GITHUB_CLIENT_SECRET` (Secrets Manager in AWS, never in git), `IACP_GITHUB_CALLBACK_URL` (identical to the registered URL), `IACP_GITHUB_ORG`, optionally `IACP_AUTH_REDIRECT_ALLOWLIST`, and create the GitHub teams above.

Enforcement points: API middleware (`rbac.Decide`), GitHub branch protection/CODEOWNERS (`environments/production/<team>/** @acme/<team>-maintainers`), GitHub Environment `production` required reviewers (`@acme/*-maintainers` via team-scoped rule), and OPA pre-apply gate (§6.8). The API and GitHub configurations are generated from the same team list so they cannot diverge; a nightly job diffs them.

## 8. Policy exception workflow

1. **Request.** A maintainer calls `POST /v1/policy-exceptions` (or clicks *Request exception* on a failed rule in the PR comment): `rule_id`, `stack_id`, optional `resource_address`, `reason`, compensating control, `ticket_url`, `expires_at` (≤ 90 days).
2. **Review.** `@acme/security` is notified; an approver (≠ requester, enforced by DB constraint) approves or rejects with comment. Audit events `policy_exception.requested/approved/rejected`.
3. **Effect.** Approved exceptions appear in `context.json.exceptions`; rules call `f.exempt(rule_id, address)`. The PR comment lists *Waived: iacp.encryption.sqs_kms (exc-91, expires 2026-12-31)* so reviewers see it. Re-plan is triggered automatically for the waiting request.
4. **Scope limits.** Non-waivable rules: `iacp.forbidden.*`, `iacp.approval.*`, `iacp.tags.mismatch`, `iacp.s3.no_public_policy` in production. `exempt()` in `policies/opa/lib/iacp.rego` refuses `iacp.forbidden.*` and production `iacp.s3.no_public_policy` itself, whatever the exception list contains; `iacp.tags.mismatch` has no exception hook at all. Budget is not an exception — it uses budget overrides (admin).
5. **Expiry.** Worker expires exceptions at `expires_at` (status `expired`), notifies 7 days before; the next drift run evaluates policies too, so an expired exception turns the stack's compliance status red and opens a remediation task.
6. **Reporting.** Monthly exception report (count by rule/team, age, renewals) to security leadership; renewals > 2 require CISO approval.

## 9. Compliance and audit evidence

| Control objective | SOC 2 / ISO 27001 ref | The platform control | Evidence source |
|---|---|---|---|
| Changes are authorised and reviewed | CC8.1 / A.8.32 | PR + CODEOWNERS + Environment approval; plan-hash check | GitHub PR history & reviews; `audit_events` `request.approved`; `requests.approved_plan_hash` |
| Segregation of duties | CC5.1 / A.5.3 | Author cannot approve; prod needs maintainer; exception approver ≠ requester | Branch protection export; DB constraint; audit queries |
| Logical access is least privilege | CC6.1, CC6.3 / A.8.2, A.5.15 | OIDC roles, permission boundary, SCPs, read-only humans | IAM policy JSON in repo; Access Analyzer reports; credential report (no access keys) |
| No static credentials | CC6.1 / A.8.5 | OIDC only; Secrets Manager | IAM credential report; GitHub secret scan; CloudTrail `AssumeRoleWithWebIdentity` |
| Encryption at rest/in transit | CC6.7 / A.8.24 | OPA encryption rules; TLS-only bucket policies | Policy results in `plan_summaries.policy_results`; Config rules |
| Configuration baselines enforced | CC7.1 / A.8.9 | Modules + OPA + Checkov; drift detection | Policy test reports; `drift_reports` |
| Change detection / monitoring | CC7.2 / A.8.16 | Nightly drift; CloudTrail alerts; push-to-main detector | `drift_reports`; CloudWatch alarm history |
| Audit logs complete & tamper-evident | CC7.2 / A.8.15 | Hash-chained `audit_events`, S3 Object Lock anchors, per-account CloudTrail trails with log file validation → Object Lock archive | `GET /audit/verify`; anchor objects; CloudTrail digest files |
| Vendor / supply chain | CC9.2 / A.5.21 | SHA-pinned actions, lockfiles, signed images, SLSA provenance | Workflow files; cosign/attestation records |
| Cost governance | (internal FinOps) | Budgets, OPA budget rule, overrides | `cost_estimates`, `budgets.override_by` + audit `budget.override.granted` |
| Exceptions are governed | CC3.4 / A.5.36 | Time-boxed, security-approved exceptions | `policy_exceptions`, monthly report |
| Recovery capability | A1.2 / A.8.13 | Versioned state, PITR, DR drills | Backup reports, drill records |

**Evidence pack automation:** `GET /v1/audit/export` plus a quarterly `evidence.yml` workflow that collects branch-protection settings, IAM policies, SCPs, policy test coverage, exception report, drift summary and audit chain verification into a dated, signed bundle in the log-archive account.
