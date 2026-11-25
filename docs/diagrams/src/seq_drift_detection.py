"""Sequence: nightly drift detection across active stacks."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from seqlib import Sequence  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images",
                   "seq-drift-detection.png")


def build() -> Sequence:
    s = Sequence("Drift detection: nightly plan -detailed-exitcode",
                 subtitle="Read-only: drift.yml uses the iacp-plan role and never applies")
    s.participant("sched", "EventBridge\nScheduler", "aws")
    s.participant("worker", "Platform\nWorker")
    s.participant("db", "PostgreSQL", "data")
    s.participant("gh", "GitHub App", "github")
    s.participant("gha", "GitHub Actions\ndrift.yml (matrix)", "github")
    s.participant("aws", "Workload account\n(iacp-plan)", "aws")
    s.participant("api", "Platform API")
    s.participant("owner", "Stack owners\n(team)", "actor")

    s.msg("sched", "worker", "cron 02:00 UTC -> SQS drift-scan message")
    s.msg("worker", "db", "SELECT active stacks without an open lock")
    s.ret("db", "worker", "stack list (env/team/stack)")
    s.msg("worker", "gh", "workflow_dispatch drift.yml {stacks matrix}")
    s.msg("gh", "gha", "start matrix jobs (max-parallel per account)")
    s.frame("loop", "for each stack in matrix")
    s.msg("gha", "aws", "AssumeRoleWithWebIdentity (iacp-plan)")
    s.self_msg("gha", "terraform plan -detailed-exitcode -lock=false")
    s.frame("alt", "exit 0: no changes")
    s.msg("gha", "api", "POST /v1/drift {stack, status=clean}")
    s.else_("exit 2: drift detected")
    s.msg("gha", "api", "POST /v1/drift {stack, changed resources, plan JSON}")
    s.msg("api", "db", "INSERT drift_reports; stack -> drifted; audit_event")
    s.else_("exit 1: error")
    s.msg("gha", "api", "POST /v1/drift {stack, status=error, logs url}", fail=True)
    s.end()
    s.end()
    s.msg("api", "owner", "Notify: drift summary (portal badge, Slack, issue)")
    s.note("owner", "Owner chooses: reconcile (re-apply desired config via PR) "
                    "or adopt (update inputs to match reality)", span=("api", "owner"))
    return s


if __name__ == "__main__":
    build().render(OUT)
    print("wrote", os.path.normpath(OUT))
