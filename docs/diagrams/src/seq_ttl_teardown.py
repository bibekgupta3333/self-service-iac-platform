"""Sequence: ephemeral stack reaches its TTL and is destroyed."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from seqlib import Sequence  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images",
                   "seq-ttl-teardown.png")


def build() -> Sequence:
    s = Sequence("TTL teardown: ephemeral stack expiry",
                 subtitle="Destroy goes through the same PR + apply pipeline as any other change")
    s.participant("sched", "EventBridge\nScheduler", "aws")
    s.participant("worker", "Foundry\nWorker")
    s.participant("db", "PostgreSQL", "data")
    s.participant("owner", "Stack owner", "actor")
    s.participant("gh", "GitHub App /\nRepository", "github")
    s.participant("gha", "GitHub Actions\npr-validate / apply", "github")
    s.participant("aws", "Workload\naccount", "aws")

    s.msg("sched", "worker", "hourly ttl-reaper tick (SQS)")
    s.msg("worker", "db", "SELECT stacks WHERE expires_at < now() + 24h")
    s.ret("db", "worker", "expiring stacks")
    s.frame("opt", "expires within 24h and not yet warned")
    s.msg("worker", "owner", "Warning: stack expires in 24h (extend or let expire)")
    s.msg("worker", "db", "mark warned_at; audit_event(ttl.warning)")
    s.frame("opt", "owner extends TTL")
    s.msg("owner", "worker", "PATCH expires_at (via API, policy max TTL)")
    s.end()
    s.end()
    s.frame("alt", "expires_at reached")
    s.msg("worker", "db", "INSERT request(type=destroy); acquire stack lock")
    s.msg("worker", "gh", "PR: remove stack root (auto-label ttl-expired)")
    s.msg("gh", "gha", "pr-validate.yml: plan -destroy + policy checks")
    s.ret("gha", "worker", "plan_ready (destroy plan summary)")
    s.note("gh", "non-prod only: TTL PRs are auto-approved by the platform bot",
           span=("gh", "gha"))
    s.msg("worker", "gh", "approve + merge destroy PR")
    s.msg("gh", "gha", "apply.yml (destroy) on merge")
    s.msg("gha", "aws", "terraform destroy (foundry-apply role)")
    s.ret("aws", "gha", "resources deleted")
    s.msg("gha", "worker", "POST result=success (via API)")
    s.msg("worker", "db", "stack -> destroyed; request -> applied; release lock")
    s.msg("worker", "db", "audit_event(stack.destroyed, reason=ttl)")
    s.msg("worker", "owner", "Notify: stack destroyed")
    s.end()
    return s


if __name__ == "__main__":
    build().render(OUT)
    print("wrote", os.path.normpath(OUT))
