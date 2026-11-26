"""conftest ``--data`` context for a stack (input to policies/opa/lib/iacp.rego).

Until the budget-context endpoint exists (WBS 6.4), CI derives the context from
iacp.stack.json. The environment comes from the stack's path-checked metadata,
never from resource tags, so a ``development`` tag cannot relax a rule.
Exceptions are always empty here: waivers come only from the platform API.
"""

from __future__ import annotations

from typing import Any, Sequence

from .models import StackMetadata


def build_context(meta: StackMetadata, allowed_regions: Sequence[str] = ()) -> dict[str, Any]:
    """The ``{"iacp": {"context": {...}}}`` document for conftest --data."""
    context: dict[str, Any] = {
        "environment": meta.ref.environment,
        "team": meta.ref.team,
        "stack": meta.ref.stack,
        "exceptions": [],
    }
    if allowed_regions:
        context["allowed_regions"] = list(allowed_regions)
    return {"iacp": {"context": context}}
