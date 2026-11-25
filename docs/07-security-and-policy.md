# 07 — Security and Policy

> Purpose: define Foundry's threat model, identity and privilege design, secrets and supply-chain controls, policy-as-code (OPA/Rego and Checkov), the policy exception workflow, RBAC, and how controls map to audit evidence.

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
| **Portal / API** | Stolen session → impersonation. *M:* GitHub OAuth + PKCE, 15-min JWT, HttpOnly/SameSite cookies, CSRF token, re-auth for prod destroy; org SSO enforced on GitHub. | Request body tampering to bypass schema. *M:* server-side JSON Schema validation, reserved inputs not overridable, generator is pure + golden-tested. | User denies requesting a change. *M:* audit event per action with actor, IP, request ID; PR authored by bot but co-authored-by trailer + request link. | IDOR across teams. *M:* RBAC middleware on every route, team scoping in SQL (`WHERE team_id = ANY($teams)`), 404 for invisible resources. | Request floods. *M:* WAF rate rules, per-user rate limits, `POST /requests` quota, SQS buffering. | Developer escalates to maintainer to self-approve prod. *M:* roles derived from GitHub teams only, sync every 15 min + on webhook; author cannot approve own PR (branch protection). |
| **Webhook ingester** | Forged webhook advancing state (e.g. fake approval). *M:* HMAC-SHA256 signature, constant-time compare; state transitions re-verified via GitHub API (review state, merge SHA) before acting. | Replay of old delivery. *M:* `X-GitHub-Delivery` dedupe; optimistic status checks. | — | Payload logs leaking data. *M:* log only IDs, not bodies. | Webhook storm. *M:* 202 + SQS, WAF rate limit, IP allowlist from GitHub meta. | — |
| **Pipeline callbacks** | Forged plan result ("all green"). *M:* GitHub OIDC JWT verified (iss, aud, repository_id, job_workflow_ref allowlist, run_id match), fencing token. | Altered plan hash. *M:* hash also recomputed in `apply.yml` from fresh plan; API only stores, apply compares to value bound at approval. | — | — | — | Callback from PR workflow claiming apply. *M:* per-endpoint `job_workflow_ref` + `ref=refs/heads/main` requirement. |
| **GitHub App** | Key theft → bot impersonation. *M:* private key in Secrets Manager (KMS CMK), only api/worker task roles can read, rotated 90 d, App permissions minimal (contents:write, pull_requests:write, actions:write, checks:read, metadata:read, members:read). | Bot pushes directly to `main`. *M:* branch protection applies to the App (no bypass list), required checks + CODEOWNERS. | — | Repo read exposure. *M:* App installed on `acme/foundry` only. | GitHub rate limits. *M:* backoff, ETags, budget alarms. | App merges its own PRs. *M:* App has no `administration` permission; required human review. |
| **GitHub Actions workflows** | Fork PR assumes AWS role. *M:* private repo, forking disabled; trust policy pins `repository_id` + `job_workflow_ref`; plan role only for PRs. | Malicious PR edits workflow to run apply. *M:* `job_workflow_ref` must be `@refs/heads/main` for apply; CODEOWNERS on `.github/**` = platform-security; workflows pinned actions by SHA. | — | Secrets in logs / plan output. *M:* `sensitive` outputs, `::add-mask::`, plan JSON redaction step before upload, no `TF_LOG` in CI. | Runner exhaustion. *M:* concurrency groups, path filters, timeouts (plan 20 min, apply 60 min). | PR changes modules to exfiltrate via plan (`external` data source, `local-exec`). *M:* TFLint/OPA deny `data.external`, `provisioner`, `http` data source with non-allowlisted hosts; plan role read-only with deny on data-plane reads. |
| **Terraform execution / workload accounts** | Role assumed outside pipeline. *M:* OIDC-only trust; EventBridge alert on `AssumeRole*` of `foundry-apply` without OIDC. | Console changes (drift). *M:* humans read-only via SSO; nightly drift; SCPs. | Who applied what. *M:* session name `apply-<run_id>-<stack_id>`, per-account CloudTrail trails → Object Lock archive. | Over-broad resource policies. *M:* OPA/Checkov (no public S3, no `*` principals), Access Analyzer. | Accidental mass destroy. *M:* OPA denies destroy of stateful resources in prod without explicit flag + maintainer; `prevent_destroy` in modules for data stores. | Created role with admin rights. *M:* `foundry-boundary` required by SCP and apply role; `iam:PassRole` restricted to `/foundry/` path. |
| **State bucket** | Other principals read state. *M:* bucket policy per-env prefix, `aws:PrincipalOrgID`, KMS key policy with encryption-context per env. | State corruption / rollback attack. *M:* versioning, Object Ownership enforced, CRR, delete denied on `.tfstate`. | — | State contains secrets. *M:* SSE-KMS CMK, humans have no decrypt, modules use Secrets Manager refs / `manage_master_user_password`. | Lock contention. *M:* S3 lockfile + platform lock queue. | — |
| **Module registry (git tags)** | Spoofed module source. *M:* modules only from `github.com/acme/foundry` (OPA + TFLint rule on `source`); tag protection rules on `modules/*/v*`. | Tag moved to malicious commit. *M:* tag protection; generator records tag→SHA and `apply.yml` verifies SHA; release only via `module-release.yml`. | Who released. *M:* release audit event + signed tags (gitsign). | — | — | Module adds IAM wildcard. *M:* module PR review by owners + security, Checkov on examples, OPA on every consumer plan. |
| **Database** | DB credential theft. *M:* IAM DB auth for app, no static password in env; SG only from app tasks. | Audit tampering. *M:* append-only triggers, INSERT/SELECT-only grant, hash chain + S3 anchors. | — | Data exposure. *M:* encryption at rest (CMK), TLS enforced, no PII beyond GitHub login/email. | Connection exhaustion. *M:* pgxpool limits, RDS Proxy if needed. | SQL injection. *M:* sqlc parameterised queries only. |
| **Third-party actions / providers** | Typosquatted action. *M:* allowlist of actions at org level. | Compromised action/provider release. *M:* SHA-pinned actions, provider lockfile with hashes, Dependabot with review. | — | Action exfiltrates OIDC token. *M:* `id-token: write` only on jobs that need it; minimal `permissions:` per job. | — | — |

## 3. Least-privilege IAM

| Principal | Where | Can | Cannot | Notes |
|---|---|---|---|---|
| `foundry-plan` | each workload account | Read-only describe/list/get-config; read own-env state prefix (plans use `-lock=false`) | Data-plane reads (object get, secret values, DynamoDB items, SSM decrypt); any write | Trust: PR `pr-validate.yml`, `drift.yml@main`. See [02-aws-infrastructure.md](02-aws-infrastructure.md#51-foundry-plan-trust-policy-per-workload-account). |
| `foundry-apply` | each workload account | Create/update/delete catalog services; IAM roles under `/foundry/` with boundary; own-env state RW | Users/keys, OIDC providers, Organizations, CloudTrail stop, remove boundary | Trust: `apply.yml`/`ttl-reaper.yml` on `main` in the matching GitHub Environment. |
| `foundry-boundary` | managed policy | Caps roles created by stacks | See [02-aws-infrastructure.md](02-aws-infrastructure.md#6-permission-boundary) | Required by SCP on role creation. |
| `foundry-test` | sandbox account | Full catalog services in sandbox | Anything outside sandbox | `module-release.yml` Terratest; account nuked nightly with `aws-nuke`. |
| `foundry-api-task` / `foundry-worker-task` | platform | SQS, specific secrets, audit anchor PutObject, `rds-db:connect` | Anything in workload accounts; state bucket | |
| `foundry-breakglass` | each account | Admin | — | SSO permission set, MFA, 1 h, 2-person approval in IdP, P1 alert on use, post-incident reconcile PR mandatory. |
| Humans (SSO) | workload accounts | `ReadOnlyAccess` (minus data-plane) | Writes | Writes only via Foundry or break-glass. |

Review cadence: IAM Access Analyzer unused-access findings reviewed monthly; `foundry-apply` policy diffed on every change via PR with security CODEOWNER.

## 4. Secrets handling

- **Platform secrets** (GitHub App key, webhook secret, OAuth client secret, JWT signing keys, Slack webhook) live in Secrets Manager encrypted with `alias/foundry-app`; injected into ECS tasks via `secrets:` (valueFrom ARN), never baked into images or env files. Rotation: JWT keys 90 d (kid-based dual validation), webhook secret 180 d, GitHub App key 90 d (two active keys during rotation).
- **No AWS secrets in GitHub.** Repository/organisation secrets contain no AWS keys; only non-secret variables (`FOUNDRY_API_URL`, account IDs). Infracost API key is the only CI secret (scoped to `pr-validate` environment).
- **Workload secrets** are never module inputs in plain text. Modules accept secret *references* (`secret_arn`), create secrets with random values and `ignore_changes` on value, or use native managed passwords (`manage_master_user_password = true` for RDS). JSON Schema marks fields `"x-foundry-sensitive": true`; the API rejects values that look like secrets (high-entropy detector + `gitleaks` rules) in non-sensitive fields.
- **State** is the residual risk: encrypted with CMK, per-env decrypt grants for pipeline roles only, no human decrypt.
- **Plan artifacts** are redacted (sensitive values and `*_password`, `*_secret`, `*_token` attributes replaced) before upload to `foundry-plan-artifacts-*` and PR comments.
- **Logs:** `slog` redaction handler for known secret keys; Actions `::add-mask::` for any dynamically fetched value.
- **Detection:** GitHub secret scanning + push protection on the repo; `gitleaks` as a required PR check.

## 5. Supply chain

| Control | Implementation |
|---|---|
| Actions pinned by SHA | Every `uses:` references a full 40-char commit SHA with a version comment, e.g. `actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2`, `aws-actions/configure-aws-credentials@e3dd6a429d7300a6a4c196c26e071d42e0343502 # v4.0.2`. Org policy: "Allow select actions" allowlist; `pin-github-action` check in CI fails on tag refs. Dependabot updates SHAs via PR. |
| Workflow integrity | `.github/**` CODEOWNERS = `@acme/platform-security`; `permissions: {}` at top, granted per job; `id-token: write` only on AWS/callback jobs; `pull_request` (not `pull_request_target`) for untrusted code. |
| Module ref pinning | Generated `source` always `?ref=modules/<name>/vX.Y.Z`; tag protection prevents move/delete; `foundry.stack.json` stores tag→SHA; `apply.yml` runs `git rev-parse` and fails on mismatch. OPA denies non-tag refs and non-`acme/foundry` sources. |
| Provider lockfile | Each generated stack commits `.terraform.lock.hcl` produced by `terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_arm64`; CI runs `terraform init -lockfile=readonly`. Provider versions constrained by `module.yaml` ranges. |
| Provider mirror | Optional: `provider_installation { network_mirror { url = "https://tf-mirror.foundry.acme.internal/" } }` backed by S3 for availability and vetting. |
| Tool versions | `terraform`, `tflint`, `checkov`, `conftest`, `infracost` versions pinned in `.tool-versions`; downloaded binaries verified by SHA-256 in the workflow. |
| Container images | Backend/portal images built in Actions, SBOM (Syft) + vulnerability scan (Grype, fail on critical), signed with cosign keyless (Sigstore, OIDC identity of `release.yml@main`); ECS pulls by digest; ECR immutable tags. |
| Module release provenance | `module-release.yml` produces SLSA provenance attestation (`actions/attest-build-provenance`) over the module tarball; catalog records attestation URL. |
| Dependencies | Go modules via `go.sum` + `govulncheck`; npm via lockfile + `npm audit --omit=dev` threshold high; Dependabot weekly. |

## 6. Policy as code

### 6.1 Layout and evaluation

```
policies/
├── opa/
│   ├── lib/foundry.rego               # helpers: resources by type, exemptions, tags
│   ├── tags.rego                      # foundry.tags
│   ├── encryption.rego                # foundry.encryption
│   ├── s3_public.rego                 # foundry.s3
│   ├── regions_and_sizes.rego         # foundry.placement
│   ├── budget.rego                    # foundry.budget   (input = infracost JSON)
│   ├── prod_approval.rego             # foundry.approval (pre-apply gate)
│   ├── forbidden.rego                 # foundry.forbidden (external/provisioners/sources)
│   └── *_test.rego                    # opa test fixtures, 100 % rule coverage required
└── checkov/
    ├── checks/
    │   ├── sqs_redrive.py             # CKV_FDY_001
    │   └── lambda_tracing.yaml        # CKV2_FDY_002
    └── .checkov.yaml
```

`pr-validate.yml` evaluation:

```bash
terraform show -json tfplan > plan.json
curl -sf -H "Authorization: Bearer $OIDC" "$FOUNDRY_API/v1/internal/budget-context?stack_path=$STACK" > context.json
conftest test plan.json      --policy policies/opa --data context.json --namespace foundry.tags \
  --namespace foundry.encryption --namespace foundry.s3 --namespace foundry.placement --namespace foundry.forbidden \
  --output json > opa-plan.json
conftest test infracost.json --policy policies/opa --data context.json --namespace foundry.budget --output json > opa-budget.json
```

`context.json` (served by the API) — exposed in Rego as `data.foundry.context`:

```json
{
  "foundry": {
    "context": {
      "environment": "production",
      "team": "payments",
      "stack": "order-events",
      "allowed_regions": ["us-east-1", "us-west-2"],
      "budget": { "limit_usd": 2000.0, "spent_usd": 1450.10, "hard_block": true },
      "budget_override": null,
      "exceptions": [
        { "rule_id": "foundry.encryption.sqs_kms", "resource_address": "module.legacy_queue.aws_sqs_queue.this",
          "expires_at": "2026-12-31T00:00:00Z" }
      ],
      "now": "2026-11-24T10:05:00Z"
    }
  }
}
```

Rules emit `deny` (blocking) and `warn` (comment only). Each message is prefixed by a stable rule ID used by exceptions and metrics.

### 6.2 Shared library

```rego
# policies/opa/lib/foundry.rego
package lib.foundry

import rego.v1

ctx := data.foundry.context

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
package foundry.tags

import rego.v1
import data.lib.foundry as f

required := {"foundry:team", "foundry:env", "foundry:stack", "foundry:managed-by", "cost-center"}

# Resource types that do not support tags are skipped.
untaggable := {"aws_iam_role_policy", "aws_iam_role_policy_attachment", "aws_lambda_event_source_mapping",
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
	not f.exempt("foundry.tags.required", rc.address)
	m := f.msg("foundry.tags.required", rc.address, sprintf("missing tags %v", [sort(missing)]))
}

deny contains m if {
	some rc in f.changed
	tags := object.get(rc.change.after, "tags_all", {})
	tags["foundry:team"] != f.ctx.team
	m := f.msg("foundry.tags.team_mismatch", rc.address,
		sprintf("foundry:team=%q does not match owning team %q", [tags["foundry:team"], f.ctx.team]))
}
```

### 6.4 Encryption required

```rego
# policies/opa/encryption.rego
package foundry.encryption

import rego.v1
import data.lib.foundry as f

# S3: every bucket must have an SSE configuration using aws:kms.
deny contains m if {
	some b in f.changed_of_type("aws_s3_bucket")
	not bucket_has_kms(b)
	not f.exempt("foundry.encryption.s3_kms", b.address)
	m := f.msg("foundry.encryption.s3_kms", b.address, "bucket must use SSE-KMS (aws_s3_bucket_server_side_encryption_configuration with aws:kms)")
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
	not f.exempt("foundry.encryption.sqs_kms", q.address)
	m := f.msg("foundry.encryption.sqs_kms", q.address, "queue must set kms_master_key_id in staging/production")
}

deny contains m if {
	some q in f.changed_of_type("aws_sqs_queue")
	not q.change.after.kms_master_key_id
	q.change.after.sqs_managed_sse_enabled != true
	m := f.msg("foundry.encryption.sqs_any", q.address, "queue must be encrypted")
}

# RDS / ElastiCache / EBS
deny contains m if {
	some r in f.changed_of_type("aws_db_instance")
	r.change.after.storage_encrypted != true
	m := f.msg("foundry.encryption.rds", r.address, "storage_encrypted must be true")
}

deny contains m if {
	some r in f.changed_of_type("aws_elasticache_replication_group")
	not all_true([r.change.after.at_rest_encryption_enabled, r.change.after.transit_encryption_enabled])
	m := f.msg("foundry.encryption.elasticache", r.address, "at-rest and in-transit encryption required")
}

all_true(xs) if every x in xs { x == true }

# KMS keys must rotate.
deny contains m if {
	some k in f.changed_of_type("aws_kms_key")
	k.change.after.enable_key_rotation != true
	m := f.msg("foundry.encryption.kms_rotation", k.address, "enable_key_rotation must be true")
}
```

### 6.5 No public S3

```rego
# policies/opa/s3_public.rego
package foundry.s3

import rego.v1
import data.lib.foundry as f

# Every bucket needs a public access block with all four settings true.
deny contains m if {
	some b in f.changed_of_type("aws_s3_bucket")
	not has_full_pab(b)
	not f.exempt("foundry.s3.no_public", b.address)
	m := f.msg("foundry.s3.no_public", b.address, "aws_s3_bucket_public_access_block with all four flags = true is required")
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
	m := f.msg("foundry.s3.no_public_acl", a.address, sprintf("ACL %q is not allowed", [a.change.after.acl]))
}

# Bucket policies must not grant to "*" without an org or source condition.
deny contains m if {
	some p in f.changed_of_type("aws_s3_bucket_policy")
	doc := json.unmarshal(p.change.after.policy)
	some s in doc.Statement
	s.Effect == "Allow"
	wildcard_principal(s.Principal)
	not restricted(s)
	m := f.msg("foundry.s3.no_public_policy", p.address, "Allow to Principal \"*\" requires aws:PrincipalOrgID or aws:SourceArn condition")
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
package foundry.placement

import rego.v1
import data.lib.foundry as f

# Provider region (constant in generated providers.tf).
deny contains m if {
	some name, p in input.configuration.provider_config
	p.name == "aws"
	region := p.expressions.region.constant_value
	not region in f.ctx.allowed_regions
	m := f.msg("foundry.placement.region", sprintf("provider.%s", [name]),
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
	not f.exempt("foundry.placement.size", rc.address)
	m := f.msg("foundry.placement.size", rc.address,
		sprintf("%s %q not allowed in %s", [attr, size, f.ctx.environment]))
}

# Lambda memory ceiling (cost guard).
deny contains m if {
	some rc in f.changed_of_type("aws_lambda_function")
	rc.change.after.memory_size > 3008
	m := f.msg("foundry.placement.lambda_memory", rc.address, "memory_size above 3008 MB requires an exception")
}
```

### 6.7 Budget limit

Input is Infracost's JSON output (`infracost breakdown --format json --compare-to infracost-base.json`).

```rego
# policies/opa/budget.rego
package foundry.budget

import rego.v1

ctx := data.foundry.context

delta := to_number(input.diffTotalMonthlyCost)

projected := ctx.budget.spent_usd + delta

override_ok if {
	o := ctx.budget_override
	o != null
	delta <= o.max_delta_usd
	time.parse_rfc3339_ns(o.expires_at) > time.parse_rfc3339_ns(ctx.now)
}

deny contains msg if {
	ctx.budget.hard_block
	delta > 0
	projected > ctx.budget.limit_usd
	not override_ok
	msg := sprintf("[foundry.budget.exceeded] change adds %.2f %s/month; projected %.2f exceeds %s budget %.2f (remaining %.2f). Ask an admin for an override.",
		[delta, "USD", projected, ctx.team, ctx.budget.limit_usd, ctx.budget.limit_usd - ctx.budget.spent_usd])
}

warn contains msg if {
	delta > 0
	projected > 0.8 * ctx.budget.limit_usd
	projected <= ctx.budget.limit_usd
	msg := sprintf("[foundry.budget.near_limit] projected spend %.2f is above 80%% of budget %.2f", [projected, ctx.budget.limit_usd])
}

# Single change sanity cap regardless of budget headroom.
deny contains msg if {
	delta > 5000
	not override_ok
	msg := sprintf("[foundry.budget.single_change_cap] change adds %.2f/month (> 5000); admin override required", [delta])
}
```

### 6.8 Production requires approvals (pre-apply gate)

Branch protection + CODEOWNERS + the `production` GitHub Environment enforce approvals natively. As defence in depth (e.g. a misconfigured protection rule), `apply.yml` evaluates a pre-apply policy against the request record returned by `GET /v1/internal/requests/by-path` (with budget/exception context from `GET /v1/internal/budget-context`) (`approvals` populated from GitHub reviews and mapped to Foundry roles):

```rego
# policies/opa/prod_approval.rego
package foundry.approval

import rego.v1

ctx := data.foundry.context

maintainer_approvals := {a.login |
	some a in input.request.approvals
	a.state == "APPROVED"
	a.role in {"maintainer", "admin"}
	a.login != input.request.author
	a.login != "foundry-approver"   # machine user never counts in production
}

destructive if {
	some rc in input.plan.resource_changes
	"delete" in rc.change.actions
}

deny contains "[foundry.approval.prod_min] production change needs >= 1 maintainer approval from someone other than the author" if {
	ctx.environment == "production"
	count(maintainer_approvals) < 1
}

deny contains "[foundry.approval.prod_destructive] production change with deletes/replacements needs >= 2 maintainer approvals" if {
	ctx.environment == "production"
	destructive
	count(maintainer_approvals) < 2
}

deny contains "[foundry.approval.plan_hash] plan differs from the approved plan" if {
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
	msg := sprintf("[foundry.approval.stateful_destroy] %s would be deleted; request must set allow_stateful_destroy", [rc.address])
}
```

### 6.9 Forbidden constructs

```rego
# policies/opa/forbidden.rego
package foundry.forbidden

import rego.v1

deny contains msg if {
	some r in input.configuration.root_module.module_calls[name]
	src := r.source
	not regex.match(`^git::https://github\.com/acme/foundry\.git//modules/[a-z0-9-]+\?ref=modules/[a-z0-9-]+/v\d+\.\d+\.\d+$`, src)
	msg := sprintf("[foundry.forbidden.module_source] module.%s source %q is not a pinned Foundry module", [name, src])
}

deny contains msg if {
	some rc in input.resource_changes
	rc.type in {"external", "http"}
	rc.mode == "data"
	msg := sprintf("[foundry.forbidden.data_source] %s is not allowed", [rc.address])
}

deny contains msg if {
	some rc in input.resource_changes
	rc.type in {"null_resource", "terraform_data"}
	msg := sprintf("[foundry.forbidden.provisioner_carrier] %s is not allowed", [rc.address])
}
```

Policy tests (`opa test policies/opa -v --coverage`) run in `pr-validate.yml` whenever `policies/**` changes; coverage gate 100 % of rules.

### 6.10 Custom Checkov checks

Checkov runs on the generated root module **and** on each module's `examples/` during release (`checkov -d . --external-checks-dir policies/checkov/checks --download-external-modules true --framework terraform`).

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
        if tags.get("foundry:role") == "dlq":
            return CheckResult.PASSED
        redrive = conf.get("redrive_policy")
        if redrive and redrive[0]:
            return CheckResult.PASSED
        # Redrive may be configured via the separate resource; Checkov graph check below covers that.
        self.details.append("Queue has no redrive_policy; add a DLQ or tag foundry:role=dlq")
        return CheckResult.FAILED


check = SQSQueueHasRedrivePolicy()
```

YAML graph check — Lambda functions must enable X-Ray tracing and be attached to a role carrying the Foundry boundary:

```yaml
# policies/checkov/checks/lambda_tracing.yaml
metadata:
  id: "CKV2_FDY_002"
  name: "Ensure Lambda functions enable active tracing and use a Foundry-bounded role"
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
      value: "policy/foundry-boundary"
```

`.checkov.yaml` sets `soft-fail-on: LOW`, `hard-fail-on: MEDIUM,HIGH,CRITICAL`, and skips checks that duplicate OPA rules (to keep one source of truth per control). Inline `#checkov:skip` comments are **not** honoured in `environments/**` (generated code); waivers go through the exception workflow.

## 7. RBAC matrix

Roles are per team, derived from GitHub team membership: `<team>-viewers` → viewer, `<team>` (member) → developer, `<team>-maintainers` → maintainer. `admin` = member of `@acme/foundry-admins` (platform-wide). `security` approvers = `@acme/security` (orthogonal capability).

| Action | viewer | developer | maintainer | admin | security |
|---|:-:|:-:|:-:|:-:|:-:|
| Browse catalog, module docs | ✓ | ✓ | ✓ | ✓ | ✓ |
| View team stacks, requests, plans, costs, drift | ✓ | ✓ | ✓ | ✓ | ✓ (all teams) |
| Create request (create/update/upgrade) — development, staging | | ✓ | ✓ | ✓ | |
| Create request — production | | ✓ | ✓ | ✓ | |
| Create destroy request — development, staging | | ✓ | ✓ | ✓ | |
| Create destroy request — production | | | ✓ | ✓ | |
| Approve PR (CODEOWNERS) — non-prod | | ✓ (not own) | ✓ | ✓ | |
| Auto-approve TTL-expiry destroy PR — non-prod only | `foundry-approver` machine user only (system actor), audited as `request.auto_approved`; never in production | | | | |
| Approve PR / Environment — production | | | ✓ (not own) | ✓ | |
| Cancel own request | | ✓ | ✓ | ✓ | |
| Cancel others' request | | | ✓ | ✓ | |
| Retry failed apply / rollback | | | ✓ | ✓ | |
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

**Automated approvals.** GitHub Apps cannot be code owners, so the Foundry GitHub App never approves PRs. Automated approvals exist only in non-prod — notably **TTL-expiry destroy PRs** (and development auto-approval rules described in [05-automation-pipeline.md](05-automation-pipeline.md)) — and are submitted by a separate machine user `foundry-approver` (member of `@acme/foundry-approvers`, CODEOWNER only for `environments/development/**` and `environments/staging/**`; token in Secrets Manager, readable only by the worker task role; every use audited). Production is never auto-approved: `environments/production/**` CODEOWNERS and the `production` Environment reviewers exclude `@acme/foundry-approvers`, and the pre-apply OPA gate (§6.8) ignores approvals from `foundry-approver`.

Enforcement points: API middleware (`rbac.Authorize`), GitHub branch protection/CODEOWNERS (`environments/production/<team>/** @acme/<team>-maintainers`), GitHub Environment `production` required reviewers (`@acme/*-maintainers` via team-scoped rule), and OPA pre-apply gate (§6.8). The API and GitHub configurations are generated from the same team list so they cannot diverge; a nightly job diffs them.

## 8. Policy exception workflow

1. **Request.** A maintainer calls `POST /v1/policy-exceptions` (or clicks *Request exception* on a failed rule in the PR comment): `rule_id`, `stack_id`, optional `resource_address`, `reason`, compensating control, `ticket_url`, `expires_at` (≤ 90 days).
2. **Review.** `@acme/security` is notified; an approver (≠ requester, enforced by DB constraint) approves or rejects with comment. Audit events `policy_exception.requested/approved/rejected`.
3. **Effect.** Approved exceptions appear in `context.json.exceptions`; rules call `f.exempt(rule_id, address)`. The PR comment lists *Waived: foundry.encryption.sqs_kms (exc-91, expires 2026-12-31)* so reviewers see it. Re-plan is triggered automatically for the waiting request.
4. **Scope limits.** Non-waivable rules: `foundry.forbidden.*`, `foundry.approval.*`, `foundry.tags.team_mismatch`, `foundry.s3.no_public_policy` in production. Budget is not an exception — it uses budget overrides (admin).
5. **Expiry.** Worker expires exceptions at `expires_at` (status `expired`), notifies 7 days before; the next drift run evaluates policies too, so an expired exception turns the stack's compliance status red and opens a remediation task.
6. **Reporting.** Monthly exception report (count by rule/team, age, renewals) to security leadership; renewals > 2 require CISO approval.

## 9. Compliance and audit evidence

| Control objective | SOC 2 / ISO 27001 ref | Foundry control | Evidence source |
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
