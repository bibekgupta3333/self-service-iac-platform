"""Keep the branch ruleset's required status checks in step with the workflows.

A required check is the ``name:`` of a job (docs/05 section 5.1). A name that no job
has leaves the check pending forever and blocks every PR; a job renamed in a workflow
silently stops being required. ``required_contexts`` and ``job_names`` are pure;
``RepositoryChecks`` reads the files.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping

import yaml

RULESET = Path(".github/rulesets/main-branch.json")
WORKFLOWS = Path(".github/workflows")


def required_contexts(ruleset: Mapping[str, Any]) -> tuple[str, ...]:
    """The ``context`` of every required status check in a ruleset document. Pure."""
    contexts: list[str] = []
    for rule in ruleset.get("rules") or []:
        if isinstance(rule, Mapping) and rule.get("type") == "required_status_checks":
            checks = (rule.get("parameters") or {}).get("required_status_checks") or []
            contexts.extend(str(c["context"]) for c in checks if isinstance(c, Mapping) and "context" in c)
    return tuple(contexts)


def job_names(workflow: Mapping[str, Any]) -> tuple[str, ...]:
    """The check name of each job: its ``name``, or the job id when it has none. Pure."""
    jobs = workflow.get("jobs") or {}
    return tuple(
        str(job.get("name") or job_id) if isinstance(job, Mapping) else str(job_id) for job_id, job in jobs.items()
    )


def problems(contexts: tuple[str, ...], names: tuple[str, ...]) -> tuple[str, ...]:
    """What is wrong with ``contexts`` given every job name in the workflows (with repeats). Pure.

    An empty list is a problem: a ruleset that requires nothing is not a gate. A required
    name that two jobs share is ambiguous (GitHub matches by name, so either job can
    satisfy it), so it is a problem too.
    """
    found = [] if contexts else ["the ruleset requires no status checks"]
    found += [f"required check {c!r} is not the name of any job in .github/workflows/" for c in contexts if c not in names]
    found += [
        f"required check {c!r} is the name of {names.count(c)} jobs; give each job a unique name"
        for c in dict.fromkeys(contexts)
        if names.count(c) > 1
    ]
    found += [f"required check {c!r} is listed more than once" for c in dict.fromkeys(contexts) if contexts.count(c) > 1]
    found += [f"required check {c!r} has a matrix expression and cannot be a stable name" for c in contexts if "${{" in c]
    return tuple(found)


@dataclass(frozen=True)
class RepositoryChecks:
    """Reads the ruleset and the workflows of a checkout."""

    root: Path

    def contexts(self) -> tuple[str, ...]:
        return required_contexts(json.loads((self.root / RULESET).read_text(encoding="utf-8")))

    def names(self) -> tuple[str, ...]:
        """Every job name in ``*.yml`` and ``*.yaml`` workflows, repeats included."""
        directory = self.root / WORKFLOWS
        paths = sorted([*directory.glob("*.yml"), *directory.glob("*.yaml")])
        found: list[str] = []
        for path in paths:
            found.extend(job_names(yaml.safe_load(path.read_text(encoding="utf-8")) or {}))
        return tuple(found)

    def problems(self) -> tuple[str, ...]:
        return problems(self.contexts(), self.names())
