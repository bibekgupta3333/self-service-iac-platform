"""Sequence: developer provisions a new stack from the portal (PR stage)."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from seqlib import Sequence  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images",
                   "seq-provision-request.png")


def build() -> Sequence:
    s = Sequence("Provision request: portal to reviewed pull request",
                 subtitle="Terraform never runs in the backend; GitHub Actions plans "
                          "and reports back with a GitHub OIDC token")
    s.participant("dev", "Developer", "actor")
    s.participant("portal", "Portal\n(Next.js)")
    s.participant("api", "Foundry API\n(Go)")
    s.participant("gen", "Config\nGenerator")
    s.participant("db", "PostgreSQL", "data")
    s.participant("gh", "GitHub App /\nRepository", "github")
    s.participant("gha", "GitHub Actions\npr-validate.yml", "github")
    s.participant("aws", "Workload account\n(foundry-plan role)", "aws")

    s.msg("dev", "portal", "Pick package s3 v1.2.0, fill typed inputs")
    s.msg("portal", "api", "POST /v1/requests {module, version, env, inputs}")
    s.self_msg("api", "AuthN session + RBAC (team role >= developer)")
    s.msg("api", "db", "Validate inputs against module_versions.inputs_schema")
    s.msg("api", "db", "INSERT request (draft); acquire stack lock row")
    s.msg("api", "gen", "Render root module")
    s.ret("gen", "api", "main.tf, backend.tf, versions.tf (pinned ?ref=modules/s3/v1.2.0)")
    s.msg("api", "gh", "Create branch, commit files, open PR, add labels")
    s.ret("gh", "api", "PR #123 opened")
    s.msg("api", "db", "request -> pr_open; audit_event(request.created)")
    s.ret("api", "portal", "201 Created {request_id, pr_url}")

    s.msg("gh", "api", "webhook pull_request.opened (HMAC verified -> SQS)")
    s.msg("gh", "gha", "Trigger pr-validate.yml on PR")
    s.msg("gha", "api", "POST /v1/runs (OIDC JWT): status=validating")
    s.self_msg("gha", "fmt -> validate -> TFLint -> Checkov")
    s.msg("gha", "aws", "AssumeRoleWithWebIdentity (foundry-plan)")
    s.ret("aws", "gha", "short-lived read-only credentials")
    s.self_msg("gha", "terraform plan -out; show -json")
    s.self_msg("gha", "OPA/conftest on plan JSON; Infracost + budget policy")

    s.frame("alt", "all checks pass")
    s.msg("gha", "gh", "PR comment: plan summary, cost delta, policy results")
    s.msg("gha", "api", "POST plan summary + cost + plan hash")
    s.msg("api", "db", "plan_summaries, cost_estimates; request -> plan_ready")
    s.else_("a check fails")
    s.msg("gha", "gh", "Failing check + annotated PR comment", fail=True)
    s.msg("gha", "api", "POST status=failed (reason, step)", fail=True)
    s.end()

    s.frame("loop", "until terminal status")
    s.msg("portal", "api", "GET /v1/requests/{id} (poll / SSE)")
    s.ret("api", "portal", "status, checks, plan + cost summary")
    s.end()
    s.ret("portal", "dev", "Plan ready for review; link to PR")
    s.note("gh", "Merge needs green checks + CODEOWNERS approval; production also "
                 "needs GitHub Environment approver", span=("gh", "aws"))
    return s


if __name__ == "__main__":
    build().render(OUT)
    print("wrote", os.path.normpath(OUT))
