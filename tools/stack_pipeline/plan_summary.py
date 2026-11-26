"""Plan summary for the sticky PR comment (and, later, the platform API report).

Everything here is pure: plan JSON, conftest JSON, step outcomes and an optional
Infracost estimate go in, a ``PlanSummary`` value and its Markdown rendering come out.
"""

from __future__ import annotations

import hashlib
import json
import re
from collections import Counter
from dataclasses import dataclass, field
from typing import Any, Mapping, Sequence

from .cost import CostEstimate

COMMENT_MARKER = "<!-- iacp-plan:{id} -->"
MAX_PLAN_TEXT = 40_000  # GitHub comments are capped at 65 536 characters
ACTION_ORDER = ("create", "update", "replace", "delete")
_SYMBOLS = {"create": "+", "update": "~", "replace": "-/+", "delete": "-"}


@dataclass(frozen=True)
class ResourceChange:
    address: str
    action: str


@dataclass(frozen=True)
class PolicyResult:
    failures: tuple[str, ...] = ()
    warnings: tuple[str, ...] = ()
    evaluated: bool = False

    @property
    def passed(self) -> bool:
        return self.evaluated and not self.failures


@dataclass(frozen=True)
class PlanSummary:
    stack_id: str
    stack_path: str
    changes: tuple[ResourceChange, ...] = ()
    policy: PolicyResult = PolicyResult()
    steps: Mapping[str, str] = field(default_factory=dict)
    plan_hash: str | None = None
    # Informational only (WBS 5.5): never part of ``ok``.
    cost: CostEstimate | None = None

    @property
    def counts(self) -> dict[str, int]:
        tally = Counter(c.action for c in self.changes)
        return {action: tally.get(action, 0) for action in ACTION_ORDER}

    @property
    def ok(self) -> bool:
        return all(outcome in ("success", "skipped") for outcome in self.steps.values()) and self.policy.passed

    def to_dict(self) -> dict[str, Any]:
        """Machine-readable form (the payload WBS 4.10 / 5.6 will report to the API)."""
        return {
            "stack_id": self.stack_id,
            "stack_path": self.stack_path,
            "ok": self.ok,
            "plan_hash": self.plan_hash,
            "counts": self.counts,
            "steps": dict(self.steps),
            "policy": {"failures": list(self.policy.failures), "warnings": list(self.policy.warnings)},
            "changes": [{"address": c.address, "action": c.action} for c in self.changes],
            "cost": self.cost.to_dict() if self.cost is not None else None,
        }


# ---- parsing ------------------------------------------------------------------


def classify(actions: Sequence[str]) -> str:
    """Terraform's action list as one word: create, update, delete, replace, read or no-op."""
    acts = set(actions)
    if {"create", "delete"} <= acts:
        return "replace"
    for action in ("create", "update", "delete", "read"):
        if action in acts:
            return action
    return "no-op"


def resource_changes(plan: Mapping[str, Any]) -> tuple[ResourceChange, ...]:
    """Managed resources that change (data reads and no-ops left out), sorted by address."""
    changes = (
        ResourceChange(rc["address"], classify(rc.get("change", {}).get("actions", [])))
        for rc in plan.get("resource_changes", [])
        if rc.get("mode", "managed") == "managed"
    )
    return tuple(sorted((c for c in changes if c.action in ACTION_ORDER), key=lambda c: c.address))


def plan_hash(plan: Mapping[str, Any]) -> str:
    """Hash of what the plan does, ignoring timestamps and run metadata (docs/05 section 4.2)."""
    normalized = [
        {"address": rc.get("address"), "actions": rc.get("change", {}).get("actions"), "after": rc.get("change", {}).get("after")}
        for rc in plan.get("resource_changes", [])
    ]
    return hashlib.sha256(json.dumps(normalized, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def policy_result(conftest_output: Sequence[Mapping[str, Any]] | None) -> PolicyResult:
    """Parse ``conftest test --output json``; None means conftest did not run."""
    if conftest_output is None:
        return PolicyResult()
    return PolicyResult(
        failures=_messages(conftest_output, "failures"),
        warnings=_messages(conftest_output, "warnings"),
        evaluated=True,
    )


def _messages(conftest_output: Sequence[Mapping[str, Any]], kind: str) -> tuple[str, ...]:
    return tuple(sorted({m.get("msg", "") for result in conftest_output for m in result.get(kind) or []}))


def summarize(
    stack_id: str,
    stack_path: str,
    plan: Mapping[str, Any] | None,
    conftest_output: Sequence[Mapping[str, Any]] | None,
    steps: Mapping[str, str],
    cost: CostEstimate | None = None,
) -> PlanSummary:
    return PlanSummary(
        stack_id=stack_id,
        stack_path=stack_path,
        changes=resource_changes(plan) if plan is not None else (),
        policy=policy_result(conftest_output),
        steps=dict(steps),
        plan_hash=plan_hash(plan) if plan is not None else None,
        cost=cost,
    )


# ---- rendering ----------------------------------------------------------------


def _truncate(text: str, limit: int = MAX_PLAN_TEXT) -> str:
    return text if len(text) <= limit else text[:limit] + f"\n... truncated ({len(text) - limit} more characters; see the job log)"


def _fence(text: str) -> str:
    # A fence longer than every backtick run anywhere in the text cannot be closed
    # early, whatever the indentation or trailing spaces of a line.
    longest = max((len(run) for run in re.findall(r"`+", text)), default=0)
    ticks = "`" * max(3, longest + 1)
    return f"{ticks}text\n{text}\n{ticks}"


def render_markdown(summary: PlanSummary, plan_text: str | None = None, commit: str = "", run_url: str = "") -> str:
    counts = summary.counts
    status = "passed" if summary.ok else "failed"
    lines = [
        COMMENT_MARKER.format(id=summary.stack_id),
        f"### pr-validate: `{summary.stack_path}` ({status})",
        "",
        "| Step | Result |",
        "|------|--------|",
        *(f"| {name} | {outcome} |" for name, outcome in summary.steps.items()),
        f"| policy (conftest) | {'passed' if summary.policy.passed else 'failed' if summary.policy.evaluated else 'not run'} |",
        "",
    ]
    if summary.plan_hash:
        lines.append(
            f"**Plan:** {counts['create']} to add, {counts['update']} to change, "
            f"{counts['replace']} to replace, {counts['delete']} to destroy. "
            f"Plan hash `{summary.plan_hash[:16]}`."
        )
        if summary.changes:
            lines += ["", "<details><summary>Resource changes</summary>", ""]
            lines += [f"- `{_SYMBOLS[c.action]}` `{c.address}` ({c.action})" for c in summary.changes]
            lines += ["", "</details>"]
    else:
        lines.append("**Plan:** not produced (an earlier step failed).")
    if summary.cost is not None:
        lines += ["", summary.cost.render_markdown()]
    if summary.policy.failures or summary.policy.warnings:
        lines += ["", "**Policy findings**", ""]
        lines += [f"- deny: {m}" for m in summary.policy.failures]
        lines += [f"- warn: {m}" for m in summary.policy.warnings]
    if plan_text:
        lines += ["", "<details><summary>terraform plan output</summary>", "", _fence(_truncate(plan_text)), "", "</details>"]
    footer = " · ".join(part for part in (f"commit `{commit[:12]}`" if commit else "", f"[workflow run]({run_url})" if run_url else "") if part)
    if footer:
        lines += ["", f"<sub>{footer}</sub>"]
    return "\n".join(lines) + "\n"
