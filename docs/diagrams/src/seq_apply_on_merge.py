"""Sequence: approved PR is merged and apply.yml applies the stack."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from seqlib import Sequence  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images",
                   "seq-apply-on-merge.png")


def build() -> Sequence:
    s = Sequence("Apply on merge: approved plan to applied stack",
                 subtitle="Only the approved plan hash may be applied; one mutation per stack at a time")
    s.participant("rev", "Reviewer /\nMaintainer", "actor")
    s.participant("gh", "GitHub\nRepository", "github")
    s.participant("gha", "GitHub Actions\napply.yml", "github")
    s.participant("api", "Foundry API\n+ Worker")
    s.participant("db", "PostgreSQL", "data")
    s.participant("sts", "AWS STS\n(workload acct)", "aws")
    s.participant("state", "S3 state bucket\n(platform acct)", "aws")
    s.participant("aws", "Workload\nresources", "aws")

    s.msg("rev", "gh", "Approve (CODEOWNERS) and merge PR #123 to main")
    s.note("gh", "production: GitHub Environment approval gates the job", span=("gh", "gha"))
    s.msg("gh", "gha", "push to main -> apply.yml for changed stacks only")
    s.msg("gha", "api", "GET /v1/internal/requests/by-path (OIDC JWT, stack_path, head_sha)")
    s.msg("api", "db", "Acquire per-stack lock row (SELECT ... FOR UPDATE)")
    s.frame("alt", "stack already locked")
    s.ret("api", "gha", "409 queued: job waits, re-polls")
    s.end()
    s.msg("api", "db", "request -> applying; stack -> updating / provisioning")
    s.ret("api", "gha", "200 lock + approved plan hash + fencing token")
    s.msg("gha", "sts", "AssumeRoleWithWebIdentity (foundry-apply; repo+env conditions)")
    s.ret("sts", "gha", "short-lived creds (boundary foundry-boundary)")
    s.msg("gha", "state", "terraform init; acquire lockfile (use_lockfile)")
    s.self_msg("gha", "Re-plan; hash normalised plan JSON")

    s.frame("alt", "hash matches approved plan")
    s.msg("gha", "aws", "terraform apply plan.tfplan")
    s.msg("gha", "state", "write new state version (SSE-KMS, versioned)")
    s.self_msg("gha", "Smoke tests (Terratest for ephemeral envs)")
    s.msg("gha", "api", "POST /v1/runs: outputs, resources, result=success")
    s.msg("api", "db", "stack_resources + outputs; request -> applied; stack -> active")
    s.msg("api", "db", "audit_event(stack.applied, hash-chained); release lock")
    s.else_("hash mismatch (drift or new commit since approval)")
    s.msg("gha", "api", "POST result=plan_mismatch; nothing applied", fail=True)
    s.msg("api", "db", "request -> failed; release lock; ask for re-plan", fail=True)
    s.else_("apply error")
    s.msg("gha", "api", "POST result=failed (error class, logs url)", fail=True)
    s.msg("api", "db", "stack -> failed; request -> failed; audit_event", fail=True)
    s.frame("opt", "transient error (throttling, eventual consistency)")
    s.msg("api", "gha", "workflow_dispatch re-run (max 2, backoff)")
    s.end()
    s.frame("opt", "permanent error")
    s.msg("api", "gh", "Open revert PR to previous approved config (rollback)")
    s.note("state", "last resort: restore prior state version from S3", span=("state", "aws"))
    s.end()
    s.end()
    s.ret("api", "rev", "Notify: applied / failed (portal, Slack, PR comment)")
    return s


if __name__ == "__main__":
    build().render(OUT)
    print("wrote", os.path.normpath(OUT))
