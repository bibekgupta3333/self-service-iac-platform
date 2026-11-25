# ADR-0006: OPA/conftest on plan JSON plus Checkov

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team, Security
- Related: [07-security-and-policy.md](../07-security-and-policy.md), [05-automation-pipeline.md](../05-automation-pipeline.md)

## Context

We need guardrails that block insecure or non-compliant infrastructure before it is applied, covering both generic cloud best practices (encryption, public exposure, logging) and Foundry-specific rules (approved module sources, exact version pins, mandatory tags, environment restrictions, budgets, yanked versions). Static HCL scanning misses values that only resolve at plan time (computed defaults, module internals, variables); plan-based checks miss nothing that Terraform will do.

## Decision

Use two complementary layers in `pr-validate.yml`:

1. **Checkov** (static, on the stack directory with external modules downloaded) for broad, maintained best-practice coverage, plus custom checks in `policies/checkov/` (`FOUNDRY_*`). Failures at HIGH/CRITICAL or any custom check block merge.
2. **OPA via conftest** on `terraform show -json` plan output for organisation-specific, context-aware rules. Policies live in `policies/opa/`, receive platform context (team, role, budget, module catalog status) as data, and are unit-tested with `opa test`.

Infracost output is evaluated by the same OPA engine for budget rules.

## Consequences

Positive:
- Plan-level policies see the real resource attributes and actions (create/update/delete/replace), enabling rules like "no replace of stateful resources".
- Checkov gives hundreds of maintained checks without writing them.
- One policy language (Rego) for plan, cost and drift-severity rules; policies are code-reviewed and versioned.

Negative:
- Two tools to maintain and occasionally duplicate findings; suppressions must be governed (inline with justification and ticket).
- Rego has a learning curve; mitigated by templates and tests.
- Plan JSON may include sensitive values; artifacts are retained briefly and access-controlled.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Checkov only | Weak for context-aware and plan-action rules (budget, RBAC, replace detection) |
| OPA only | Would require re-implementing broad best-practice coverage |
| HashiCorp Sentinel | Requires Terraform Cloud/Enterprise |
| tfsec / Trivy config | Comparable to Checkov; Checkov chosen for custom Python checks and plan support; can be swapped |
| AWS Config / SCPs only | Detective or coarse preventive controls; kept as defence in depth, not as the PR gate |
