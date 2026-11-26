"""conftest ``--data`` context for a stack (input to policies/opa/lib/iacp.rego).

Until the budget-context endpoint is wired into the validate job, that job derives the
context from iacp.stack.json. The environment comes from the stack's path-checked
metadata, never from resource tags, so a ``development`` tag cannot relax a rule.
Exceptions are always empty here: waivers come only from the platform API.

The budget gate (WBS 8.5, policies/opa/budget.rego) uses a second context, built by
``build_budget_context`` in a job that runs only trusted code (docs/05, docs/07 6.7):

- identity (environment, team, stack) comes from the stack root *path* the detect job
  derived, never from a file in the pull request's tree;
- the team's budget and any override come from the Platform API's budget-context
  document, which must name the same stack;
- the monthly cost estimate is one plain number taken from the Infracost output of the
  isolated cost job, and nothing else can set it: a value already present in the API
  document or anywhere else is discarded;
- ``budget_gate`` is set, so a missing estimate counts (the policy denies it in staging
  and production unless an override applies).
"""

from __future__ import annotations

from typing import Any, Mapping, Sequence

from .cost import CostEstimate
from .models import StackMetadata, StackRef

COST_CURRENCY = "USD"
# Keys only this module may set in the budget gate's context.
_GATE_KEYS = ("budget_gate", "estimated_monthly_cost_usd")
# Keys the Platform API owns: copied from its document when present.
_API_KEYS = ("allowed_regions", "budget", "budget_override", "current_committed_usd", "exceptions",
             "yanked_module_versions", "now")


class ContextError(ValueError):
    """A context document is unusable (for example the API answered for another stack)."""


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


def estimated_monthly_usd(estimate: CostEstimate | None) -> int | float | None:
    """The stack's monthly cost as a plain JSON number, or None when it cannot be used.

    None for no estimate, no total, a negative total, or a currency other than USD (a
    budget is in USD and no exchange rate is assumed): the policy then treats the cost
    as missing rather than as free. Pure.
    """
    if estimate is None or estimate.monthly is None or estimate.currency != COST_CURRENCY:
        return None
    if estimate.monthly < 0:
        return None
    value = float(estimate.monthly)
    return int(value) if value.is_integer() else value


def with_budget_gate(document: Mapping[str, Any], estimate: CostEstimate | None) -> dict[str, Any]:
    """``document`` with ``budget_gate`` on and the estimate set (or removed). Pure, idempotent.

    Whatever ``document`` already held under the two gate keys is replaced, so the estimate
    can only come from ``estimate``.
    """
    iacp = document.get("iacp") if isinstance(document.get("iacp"), Mapping) else {}
    context = dict(iacp.get("context") if isinstance(iacp.get("context"), Mapping) else {})
    for key in _GATE_KEYS:
        context.pop(key, None)
    context["budget_gate"] = True
    monthly = estimated_monthly_usd(estimate)
    if monthly is not None:
        context["estimated_monthly_cost_usd"] = monthly
    return {**document, "iacp": {**iacp, "context": context}}


def merge_api_context(ref: StackRef, base: Mapping[str, Any], api_document: Mapping[str, Any]) -> dict[str, Any]:
    """The path-derived ``base`` document with the API's budget, override and clock added. Pure.

    The API's document must name the same environment, team and stack as ``ref``; if it does
    not, the answer was for another stack and ``ContextError`` is raised. Only the keys the
    API owns are taken from it.
    """
    api = (api_document.get("iacp") or {}).get("context") if isinstance(api_document.get("iacp"), Mapping) else None
    if not isinstance(api, Mapping):
        raise ContextError("the budget context has no iacp.context object")
    for key, want in (("environment", ref.environment), ("team", ref.team), ("stack", ref.stack)):
        if api.get(key) != want:
            raise ContextError(f"the budget context is for {key} {api.get(key)!r}, not {want!r}")
    if api.get("stack_path") not in (None, ref.path):
        raise ContextError(f"the budget context is for {api.get('stack_path')!r}, not {ref.path!r}")
    context = dict(base["iacp"]["context"])
    context.update({key: api[key] for key in _API_KEYS if key in api})
    return {"iacp": {"context": context}}


def build_budget_context(
    ref: StackRef,
    estimate: CostEstimate | None,
    api_document: Mapping[str, Any] | None = None,
    allowed_regions: Sequence[str] = (),
) -> dict[str, Any]:
    """The conftest context for the budget gate of the stack root ``ref``. Pure.

    Without ``api_document`` (the API is not configured, ADR-0009) there is no budget, and
    the policy has nothing to enforce.
    """
    context: dict[str, Any] = {
        "environment": ref.environment,
        "team": ref.team,
        "stack": ref.stack,
        "stack_path": ref.path,
        "exceptions": [],
    }
    if allowed_regions:
        context["allowed_regions"] = list(allowed_regions)
    base: dict[str, Any] = {"iacp": {"context": context}}
    if api_document is not None:
        base = merge_api_context(ref, base, api_document)
    return with_budget_gate(base, estimate)
