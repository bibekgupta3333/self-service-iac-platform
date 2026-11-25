# 00 — Problem Analysis

> Purpose: capture the investigation behind the platform — what is broken today, who it hurts, what the platform must achieve (with measurable targets), what we considered buying, and why we chose this design.

## Contents

1. [Problem statement](#1-problem-statement)
2. [Goals and non-goals](#2-goals-and-non-goals)
3. [Personas](#3-personas)
4. [Functional requirements](#4-functional-requirements)
5. [Non-functional requirements](#5-non-functional-requirements)
6. [Key use cases](#6-key-use-cases)
7. [Constraints and assumptions](#7-constraints-and-assumptions)
8. [Build vs. buy](#8-build-vs-buy)
9. [Why this design](#9-why-this-design)
10. [Risks and mitigations](#10-risks-and-mitigations)

Related: [01-system-architecture.md](01-system-architecture.md) · [02-aws-infrastructure.md](02-aws-infrastructure.md) · [03-data-model-and-api.md](03-data-model-and-api.md) · [04-module-registry.md](04-module-registry.md) · [05-automation-pipeline.md](05-automation-pipeline.md) · [06-lifecycle-management.md](06-lifecycle-management.md) · [07-security-and-policy.md](07-security-and-policy.md) · [08-mvp-roadmap.md](08-mvp-roadmap.md)

---

## 1. Problem statement

Application teams need cloud resources (buckets, queues, functions, databases, caches) to ship features. Today the path to those resources is one of three things, all bad:

| Current path | What happens | Consequence |
|---|---|---|
| **Ticket to the platform team** | Developer files a ticket, waits in a queue, platform engineer hand-writes Terraform or clicks in the console. | Median 3–5 business days lead time; platform team spends most of its capacity on repetitive provisioning instead of paved-road work. |
| **Copy-paste Terraform** | Team copies a sibling repo's Terraform, edits it, applies from a laptop. | Dozens of slightly different S3/SQS/Lambda configurations; encryption, logging and tagging are inconsistent; nobody can answer "which buckets are public?" quickly. |
| **ClickOps** | Console changes in non-prod "just to test", which then leak into prod. | Untracked resources, no review, no cost attribution, drift between environments. |

Observed symptoms that motivated this work:

- **Manual provisioning bottleneck.** The platform team is a serialisation point; ticket queues grow faster than they drain at peak.
- **Inconsistency.** No single source of truth for "a good S3 bucket". Each copy has its own idea of versioning, KMS, lifecycle and access logging.
- **Security drift.** Long-lived IAM access keys on laptops and CI systems; ad-hoc console changes not reflected in code; encryption and public-access settings not enforced at creation time.
- **No lifecycle.** Resources are created and forgotten. Nobody upgrades module versions, ephemeral test stacks are never destroyed, drift goes unnoticed until an incident.
- **No cost visibility.** Cost appears weeks later in the bill, attributed to an account rather than a team or a change.
- **Weak audit trail.** Reconstructing who changed what and why requires correlating Slack, tickets, CloudTrail and git history by hand.

## 2. Goals and non-goals

### Goals

1. **Self-service.** A developer can request a supported resource without filing a ticket and without writing Terraform.
2. **Paved road by default.** Every provisioned resource comes from a versioned, reviewed, tested module ("package") with secure defaults.
3. **Everything via PR.** All infrastructure changes are expressed as code in the monorepo, reviewed in a pull request, and applied by automation.
4. **Guardrails, not gates.** Policy-as-code (OPA, Checkov), cost estimation and budget limits run automatically; humans approve only where risk warrants (production).
5. **Lifecycle management.** Upgrades, drift detection, TTL for ephemeral stacks, destroy, and rollback are first-class operations.
6. **Zero static credentials.** CI authenticates to AWS exclusively through GitHub OIDC.
7. **Full auditability.** Every action is attributable to a person, a request and a commit.

### Non-goals

- Replacing Terraform or building a new IaC language.
- Running Terraform inside the platform backend (execution stays in GitHub Actions; see [01-system-architecture.md](01-system-architecture.md#3-control-plane-vs-execution-plane)).
- Multi-cloud in v1. AWS only; the module/manifest model does not preclude other providers later.
- Arbitrary free-form Terraform through the portal. Teams needing bespoke infrastructure contribute a module or use a raw stack with platform review.
- Application deployment (container image rollout, Lambda code releases). The platform provisions infrastructure; app CD stays with existing tooling.
- A general-purpose developer portal/service catalog (ownership graph, docs hub). The platform can later be surfaced as a Backstage plugin.

## 3. Personas

| Persona | Needs | Pain today | What the platform gives them |
|---|---|---|---|
| **App developer** (Priya, backend engineer) | An SQS queue + DLQ and a Lambda consumer for a new feature, in dev today and prod next sprint. | Waits days on a ticket; does not know Terraform well; unsure which settings security requires. | Catalog of packages, typed form, PR opened for her, plan + cost in the PR comment, status in the portal. |
| **Platform engineer** (Marco) | Encode best practice once; stop hand-provisioning; keep the estate upgradable. | Most time spent on tickets; 40 divergent bucket configurations; upgrades are manual archaeology. | Module registry with SemVer, release pipeline, fleet-wide view of versions in use, drift reports, upgrade requests. |
| **Security engineer** (Aisha) | Prove encryption, no public data, least privilege, no static keys; investigate incidents fast. | Findings discovered after deployment; long-lived keys; patchy audit trail. | Policies enforced pre-merge, OIDC-only CI, permission boundary, hash-chained audit log, CloudTrail correlation by request ID. |
| **Team lead / FinOps** (Dan) | Know what the team runs and what it costs; stop surprise spend. | Cost visible only on the monthly bill per account. | Per-change cost delta in the PR, per-team monthly budgets with hard block, cost per team dashboards. |

## 4. Functional requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-01 | Users authenticate with GitHub OAuth; team membership and role derive from GitHub teams. | Must |
| FR-02 | Catalog lists modules, versions, input schema, outputs, owners, cost hints, deprecation state, changelog. | Must |
| FR-03 | Portal renders a typed form from the module's `inputs` JSON Schema and validates client- and server-side. | Must |
| FR-04 | A **request** (create / update / upgrade / destroy) generates a deterministic Terraform root module under `environments/<env>/<team>/<stack>/` and opens a PR via the GitHub App. | Must |
| FR-05 | PR pipeline runs fmt, validate, TFLint, Checkov, plan, OPA/conftest, Infracost; results are posted to the PR and to the API. | Must |
| FR-06 | Merge requires green checks + CODEOWNERS; production requires GitHub Environment approval by a maintainer. | Must |
| FR-07 | On merge, apply runs only for changed stacks, after verifying the re-plan matches the approved plan hash. | Must |
| FR-08 | Stack outputs are stored and shown in the portal; downstream stacks can consume upstream outputs (platform-injected). | Must |
| FR-09 | Only one request/pipeline mutates a stack at a time; others queue. | Must |
| FR-10 | Nightly drift detection per stack; drift reports and notifications. | Must |
| FR-11 | Ephemeral stacks carry `expires_at`; 24h warning then automatic destroy request. | Should |
| FR-12 | Version upgrade request shows upgrade path and changelog; major bumps flagged as breaking. | Should |
| FR-13 | Per-team monthly budget; Infracost delta evaluated by OPA; hard block above limit unless admin override. | Must |
| FR-14 | Append-only, hash-chained audit log of every platform action; exportable. | Must |
| FR-15 | Failed applies mark stack `failed`; transient errors auto-retry; rollback via revert PR. | Must |
| FR-16 | Module release pipeline validates, tests, tags and indexes manifests into the catalog. | Must |
| FR-17 | Policy exceptions are requestable, time-boxed and approved by security. | Should |
| FR-18 | Notifications (Slack/email) for PR opened, plan ready, approval needed, applied, failed, drift, TTL warning. | Should |

## 5. Non-functional requirements

| ID | Category | Target | How measured |
|---|---|---|---|
| NFR-01 | Lead time | Request submitted → plan posted on PR **p50 < 5 min, p95 < 10 min** | `pipeline_runs` timestamps; CloudWatch metric `iacp.request.time_to_plan` |
| NFR-02 | Lead time | Non-prod request → applied **p50 < 30 min** (no human review beyond CODEOWNERS auto-approve for dev) | `requests.applied_at - created_at` |
| NFR-03 | Governance | **100 %** of infrastructure changes in workload accounts via merged PR | CloudTrail write events by principals other than `iacp-apply` / break-glass = 0 (Config rule + Athena query) |
| NFR-04 | Security | **Zero** static AWS credentials in CI or platform | IAM credential report: no active access keys for CI users; GitHub secret scan |
| NFR-05 | Security | **100 %** of plans evaluated by OPA + Checkov before merge | required status checks on `main` |
| NFR-06 | Availability | Control plane API **99.9 %** monthly (portal/API). Execution plane availability = GitHub Actions SLA. | ALB 5xx + synthetic canary |
| NFR-07 | Durability | State: S3 11×9s + versioning; RPO **0** for state, **≤ 5 min** for Postgres (PITR); RTO **≤ 1 h** | DR drill quarterly |
| NFR-08 | Scale | 200 teams, 5 000 stacks, 500 requests/day, 50 concurrent pipelines | load test of API; Actions concurrency limits |
| NFR-09 | Drift | Drift detected within **24 h**; 100 % of active stacks scanned nightly | `drift_reports` coverage metric |
| NFR-10 | Cost | Platform run cost **< USD 1 000/month** excluding Actions minutes | AWS Cost Explorer tag `app=iacp` |
| NFR-11 | Audit | Audit log tamper-evident (hash chain verifiable), retained **≥ 400 days** hot, 7 years archive | nightly chain verification job |
| NFR-12 | Determinism | Same request inputs + module version → byte-identical generated files | golden-file tests in `backend/internal/generator` |
| NFR-13 | API latency | p95 < 300 ms for read endpoints, < 800 ms for request creation (excluding async GitHub work) | ALB target response time |
| NFR-14 | Recoverability | Failed apply surfaces actionable error within 2 min of job end; transient failures auto-retried ≤ 2 times | `pipeline_runs.status` transitions |

## 6. Key use cases

**UC-1 Provision a new stack (happy path).** Priya selects *SQS v1.3.0* and *Lambda v2.0.1* in the portal, fills inputs, targets `development`, submits. The platform acquires the stack lock, generates `environments/development/payments/order-events/`, opens PR #812. `pr-validate.yml` runs; PR comment shows +6 resources, USD 4.10/month, all policies pass. CODEOWNERS (team-maintainer) approves, merges. `apply.yml` applies; outputs (queue URL, function ARN) appear in the portal. Request status `applied`, stack `active`.

**UC-2 Promote to production.** Priya clones the stack config to `production`. Pipeline identical, but merge additionally requires the `production` GitHub Environment approval by a maintainer. OPA enforces prod-only rules (deletion protection, multi-AZ).

**UC-3 Update inputs.** Change `visibility_timeout_seconds` 30 → 60. Generator re-renders only `main.tf`; plan shows in-place update.

**UC-4 Upgrade module version.** Catalog flags `s3 v1.2.0 → v1.4.0 available` on 37 stacks. Marco triggers an upgrade request per stack (or batch); each opens a PR with changelog excerpt in the description.

**UC-5 Policy violation.** A request for a public S3 bucket fails `iacp.s3.no_public`. PR shows the violation and remediation; request stays `validating` → `failed` until inputs change or a security-approved exception exists.

**UC-6 Budget exceeded.** A request adds USD 900/month; team budget remaining is USD 300. OPA budget rule denies. Team lead asks an admin to override (audited) or raises budget.

**UC-7 Drift detected.** Nightly `drift.yml` finds a manual SG rule change in staging. Stack → `drifted`, drift report stored, team notified, "Reconcile" button opens an apply-only request to restore declared state.

**UC-8 Ephemeral environment teardown.** A preview stack created with `ttl=72h`. The hourly TTL reaper notifies the team at T-24h; at T the worker opens a destroy request. In non-prod the PR is auto-approved by the `iacp-approver` machine user and merged; resources are destroyed and stack → `destroyed`. Production TTL destroys are never auto-approved.

**UC-9 Failed apply + rollback.** Apply fails on a non-transient error mid-way. Stack → `failed`; on-call clicks *Rollback*, which opens a revert PR to the last applied config.

**UC-10 Module release.** Marco merges a change to `modules/s3`; `module-release.yml` validates, runs Terratest against LocalStack and the sandbox account, tags `modules/s3/v1.5.0`, and indexes the manifest into the catalog.

## 7. Constraints and assumptions

**Constraints**

- GitHub (Cloud, Enterprise org) is the system of record for code, identity groups and CI. GitHub Actions is the only execution runtime.
- AWS Organizations already exists with SCPs; the platform gets `platform`, `development`, `staging`, `production` accounts (plus log archive).
- Terraform 1.10.5 pinned in CI (≥ 1.10 is required for S3 native locking `use_lockfile`). AWS provider `~> 5.x`.
- Data residency: single home region (`us-east-1`) for the control plane; workload regions limited by policy.
- Small platform team (3–5 engineers) — operational footprint must be minimal (managed services only).

**Assumptions**

- Teams map 1:1 to GitHub teams; roles map from GitHub team membership/roles (see RBAC in [07-security-and-policy.md](07-security-and-policy.md#7-rbac-matrix)).
- Monorepo PR volume (hundreds/day) is acceptable with path-filtered workflows and per-stack concurrency groups.
- Self-hosted runners are not required for MVP; GitHub-hosted runners reach AWS public endpoints. Private-network modules (RDS in VPC) do not need runner network access since Terraform talks only to AWS APIs.
- Infracost Cloud Pricing API (or self-hosted pricing API) is reachable from runners.

## 8. Build vs. buy

Evaluated against our must-haves: self-service form UX over curated modules, PR-based review in GitHub, policy-as-code on plan, cost + budget enforcement, lifecycle (drift/TTL/upgrade), OIDC-only AWS auth, auditability, cost at ~200 teams.

| Option | Strengths | Gaps against our needs | Indicative cost | Verdict |
|---|---|---|---|---|
| **HCP Terraform / Terraform Cloud** | Mature runs, private registry, Sentinel/OPA, cost estimation, no-code modules, drift detection (Plus tier). | Per-resource-under-management pricing becomes significant at 5k stacks; state hosted by vendor; no-code UX limited and not PR-first; GitHub review becomes secondary to TFC runs; BSL licensing considerations. | Tiered per managed resource — high at our scale | Strong alternative; rejected for cost + PR-first UX + state custody. |
| **Spacelift** | Excellent policy engine (OPA everywhere), stack dependencies, drift, blueprints for self-service. | Seat/concurrency pricing; another control plane and UI for developers; state optionally vendor-managed; blueprints less typed than JSON-Schema forms. | Enterprise contract | Close second; revisit if build cost overruns. |
| **env0** | Self-service templates, TTL environments, cost monitoring, budgets. | Similar vendor lock-in and pricing; less flexibility in PR flow; policy model less transparent. | Per-deployment/seat | Good TTL/cost features; rejected on lock-in. |
| **Atlantis** | OSS, PR-driven plan/apply via comments, simple. | No catalog, no forms, no lifecycle (TTL, drift, upgrade), no cost/budget, applies before merge by default, needs hosting with broad credentials. | Hosting only | Covers ~20 % of scope; rejected. Our Actions pipeline gives the same PR semantics without a long-lived credentialed server. |
| **Backstage + Scaffolder** | Great portal/catalog UX, templates open PRs, plugin ecosystem. | Scaffolder is fire-and-forget: no state of stacks, no locks, no lifecycle, no budget; operating Backstage is itself a significant investment. | Hosting + team time | Complementary: the platform may expose a Backstage plugin later; not a replacement. |
| **AWS Service Catalog / Proton** | Native, IAM-integrated, launch constraints, TF support in Service Catalog. | Clunky UX; Proton is being retired by AWS; weak PR/GitOps model; cross-account portfolio sharing overhead; no Infracost/OPA integration. | Low | Rejected: poor developer UX and roadmap risk. |
| **Build (The platform)** | Exact fit: typed catalog, PR-first, OPA + Checkov + Infracost, lifecycle, OIDC, own state custody, GitHub-native review. | Engineering + maintenance cost (est. 2–3 engineers × 2 quarters to v1); we own availability of control plane. | ~USD 600–900/mo AWS + Actions minutes | **Chosen.** |

## 9. Why this design

1. **GitHub is already the review and identity plane.** Making the PR the unit of change gives review, history, revert-based rollback and CODEOWNERS for free, and keeps the audit trail where engineers already look.
2. **Execution in Actions, orchestration in the backend.** The backend never holds AWS write credentials; Actions jobs get short-lived OIDC credentials scoped to one account and one environment. A compromise of the portal cannot directly mutate infrastructure — it can at most open a PR that still has to pass checks and review.
3. **Curated modules as packages.** SemVer tags + manifest make modules behave like a package manager: discoverable, versioned, upgradable, deprecatable. The generator emits plain Terraform — no lock-in; a team can always read and, in break-glass, run the generated code.
4. **Open standards for guardrails.** OPA/Rego, Checkov, TFLint and Infracost are portable and testable in isolation.
5. **Thin, managed infrastructure.** ECS Fargate + RDS + SQS + EventBridge Scheduler keeps ops burden low for a small team.

## 10. Risks and mitigations

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | Monorepo PR volume causes CI contention and merge queue congestion. | Medium | Medium | Path filters; per-stack `concurrency` groups; GitHub merge queue; plan only changed stacks; larger runners for heavy stacks. |
| R2 | Approved plan differs from apply-time plan (state moved, upstream change). | Medium | High | Plan hash verification in `apply.yml`; mismatch aborts and re-requests review. Platform stack lock prevents concurrent platform-originated changes. |
| R3 | GitHub outage halts provisioning. | Low | Medium | Accept for non-emergency; documented break-glass runbook (assume `iacp-breakglass` with MFA, run generated code locally, reconcile via PR). |
| R4 | Over-privileged `iacp-apply` role becomes high-value target. | Medium | High | Trust bound to repo + `main` / environment; permission boundary; SCPs; separate roles per account; CloudTrail alarms on non-pipeline use. |
| R5 | Modules lag behind team needs → teams bypass the platform. | High | High | Contribution model (inner source), module scorecard, "raw stack" escape hatch with platform review, SLA for module requests. |
| R6 | Breaking module upgrades destroy/replace resources. | Medium | High | Major-version gating; plans highlight replacements; OPA denies `delete` of stateful resources in prod without explicit `allow_replace` + maintainer approval; `moved` blocks in modules. |
| R7 | Cost estimates inaccurate (usage-based services). | High | Low | Usage files per module (`infracost-usage.yml` defaults); budgets on estimated *and* actual (CUR) spend; alerts not just blocks. |
| R8 | Drift remediation clobbers intentional emergency changes. | Medium | Medium | Drift never auto-applies; human chooses reconcile vs. codify. |
| R9 | Audit log tampering by DB admin. | Low | High | Hash chain + daily anchor of chain head into S3 object-lock bucket; DB role for app cannot UPDATE/DELETE `audit_events`. |
| R10 | Build cost overrun / small team bandwidth. | Medium | Medium | MVP scope limited to S3/SQS/Lambda (see [08-mvp-roadmap.md](08-mvp-roadmap.md)); Spacelift as documented fallback. |
| R11 | State file contains secrets. | High | High | SSE-KMS CMK, narrow key policy, no human read access by default, modules avoid secret outputs (use Secrets Manager references). |
