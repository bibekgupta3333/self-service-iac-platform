"""Pure data and decisions of the TTL reaper (docs/06 section 6, docs/05 section 4.5).

Nothing here performs I/O: the due list is parsed and validated, the actions to take are
planned, API failures are classified and the run summary is rendered from plain values.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from typing import Any, Mapping, Sequence

#: Upper bound on destroy requests opened per run. The backend lists at most 500 stacks; a
#: runaway list (clock skew, bad data) must not open hundreds of destroys in one hour.
DEFAULT_MAX_TEARDOWNS = 25

#: Error codes (problem+json ``code``) after which a teardown is skipped, not failed: the
#: stack is busy with another request, was extended or vanished since the due list was read.
#: The next hourly run decides again.
SKIPPABLE_CODES = frozenset({"ttl_not_due", "ttl_backoff", "stack_busy", "stack_destroyed", "stack_not_found"})

#: Skips worth a ::warning:: because nothing else will surface them: a stack that stays busy
#: (a create that never finishes, an update stuck) is otherwise skipped silently every hour.
WARN_CODES = frozenset({"stack_busy"})

#: The stack used up its TTL destroy attempts. Not a failure of the reaper, but a person has to
#: look at the stack, so it is reported as blocked with a warning.
BLOCKED_CODE = "ttl_blocked"


class DueListError(ValueError):
    """The API's due answer does not have the documented shape."""


@dataclass(frozen=True)
class DueStack:
    """One stack of the due list."""

    stack_id: str
    path: str
    team: str
    environment: str
    expires_at: str


@dataclass(frozen=True)
class DueList:
    warnings: tuple[DueStack, ...] = ()
    teardowns: tuple[DueStack, ...] = ()


def _stack(raw: Any, where: str) -> DueStack:
    if not isinstance(raw, Mapping):
        raise DueListError(f"{where}: expected an object")
    try:
        stack_id = str(uuid.UUID(str(raw.get("stack_id", "")).strip()))
    except ValueError:
        raise DueListError(f"{where}: stack_id is not a UUID") from None
    fields = {k: raw.get(k) for k in ("path", "team", "environment", "expires_at")}
    for name, value in fields.items():
        if not isinstance(value, str) or not value:
            raise DueListError(f"{where}: {name} is missing")
    if fields["environment"] == "production":
        # The backend never lists production; a list that does is wrong, so refuse it whole.
        raise DueListError(f"{where}: production stacks are never ephemeral")
    return DueStack(stack_id=stack_id, **fields)  # type: ignore[arg-type]


def parse_due(body: Mapping[str, Any]) -> DueList:
    """Validate GET /v1/internal/ttl/due. A malformed list raises: acting on a half-understood
    answer could destroy the wrong stack. Pure."""
    lists: dict[str, tuple[DueStack, ...]] = {}
    for key in ("warnings", "teardowns"):
        raw = body.get(key)
        if not isinstance(raw, Sequence) or isinstance(raw, (str, bytes)):
            raise DueListError(f"due answer has no {key} list")
        seen: dict[str, DueStack] = {}
        for i, item in enumerate(raw):
            s = _stack(item, f"{key}[{i}]")
            seen.setdefault(s.stack_id, s)  # a repeated id is acted on once
        lists[key] = tuple(seen.values())
    both = {s.stack_id for s in lists["warnings"]} & {s.stack_id for s in lists["teardowns"]}
    # An expired stack is torn down, not warned; the backend never lists both.
    warnings = tuple(s for s in lists["warnings"] if s.stack_id not in both)
    return DueList(warnings=warnings, teardowns=lists["teardowns"])


@dataclass(frozen=True)
class Action:
    kind: str  # "teardown" or "warn"
    stack: DueStack


def plan_actions(due: DueList, max_teardowns: int = DEFAULT_MAX_TEARDOWNS) -> tuple[list[Action], list[DueStack]]:
    """The actions of one run, teardowns first, and the teardowns left for the next run
    because of the per-run cap. Pure."""
    cap = max(0, max_teardowns)
    now_down, later = due.teardowns[:cap], due.teardowns[cap:]
    actions = [Action("teardown", s) for s in now_down] + [Action("warn", s) for s in due.warnings]
    return actions, list(later)


@dataclass(frozen=True)
class Outcome:
    action: Action
    status: str  # done | already | skipped | blocked | failed | dry-run
    detail: str = ""
    code: str = ""  # the API's error code for skipped, blocked and failed outcomes

    @property
    def failed(self) -> bool:
        return self.status == "failed"


def classify_failure(status: int | None, code: str) -> str:
    """``skipped`` for the answers that only mean "not now", ``blocked`` when the stack ran out
    of destroy attempts, ``failed`` for the rest. Pure."""
    if code == BLOCKED_CODE:
        return "blocked"
    return "skipped" if code in SKIPPABLE_CODES else "failed"


def needs_attention(outcome: "Outcome") -> bool:
    """Whether the outcome deserves a ::warning:: annotation. Pure."""
    return outcome.status == "blocked" or (outcome.status == "skipped" and outcome.code in WARN_CODES)


@dataclass
class RunResult:
    outcomes: list[Outcome] = field(default_factory=list)
    deferred: list[DueStack] = field(default_factory=list)

    @property
    def failures(self) -> list[Outcome]:
        return [o for o in self.outcomes if o.failed]

    @property
    def ok(self) -> bool:
        return not self.failures


def _cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ")


def render_summary(result: RunResult) -> str:
    """Markdown for $GITHUB_STEP_SUMMARY. Pure."""
    lines = ["## TTL reaper", ""]
    if not result.outcomes and not result.deferred:
        return "\n".join(lines + ["Nothing is due."]) + "\n"
    lines += ["| Action | Stack | Result | Detail |", "|---|---|---|---|"]
    for o in result.outcomes:
        lines.append(f"| {o.action.kind} | `{_cell(o.action.stack.path)}` | {o.status} | {_cell(o.detail)} |")
    if result.deferred:
        lines += ["", f"{len(result.deferred)} more expired stack(s) wait for the next run (per-run teardown cap)."]
    return "\n".join(lines) + "\n"


def escape(text: str) -> str:
    """Escape text for a workflow command message so it cannot start a new command. Pure."""
    return text.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
