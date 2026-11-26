"""Infracost cost estimate for the plan summary (WBS 5.5, informational in the MVP).

Everything here is pure:

- ``redact_plan``: the plan JSON handed to the separate cost job, with every value
  Terraform marks sensitive replaced by null and the parts Infracost does not
  price (prior state, variables, output changes) left out;
- ``parse_infracost``: ``infracost breakdown --format json`` output as a
  ``CostEstimate``, with its one-line Markdown rendering;
- ``fold_cost``: that estimate added to an already rendered summary (JSON and
  Markdown), replacing any earlier estimate.

A cost never changes whether a summary is ``ok``; budget gating is WBS 8.5.
"""

from __future__ import annotations

import copy
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal, InvalidOperation
from typing import Any, Mapping

_SYMBOLS = {"USD": "$", "EUR": "€", "GBP": "£"}
_CENT = Decimal("0.01")
COST_LINE_PREFIX = "**Estimated cost (Infracost, informational):**"
# Top-level plan keys Infracost does not need and that can carry values verbatim.
_DROPPED_PLAN_KEYS = ("prior_state", "variables", "output_changes", "relevant_attributes")


@dataclass(frozen=True)
class CostEstimate:
    """Monthly cost of the planned resources, as Infracost priced them."""

    currency: str
    monthly: Decimal | None
    past_monthly: Decimal | None = None
    diff_monthly: Decimal | None = None
    detected: int = 0
    supported: int = 0
    usage_based: int = 0
    unsupported: int = 0

    def to_dict(self) -> dict[str, Any]:
        """JSON form for summary.json; amounts are strings so no precision is lost."""
        return {
            "currency": self.currency,
            "monthly": _str(self.monthly),
            "past_monthly": _str(self.past_monthly),
            "diff_monthly": _str(self.diff_monthly),
            "resources": {
                "detected": self.detected,
                "supported": self.supported,
                "usage_based": self.usage_based,
                "unsupported": self.unsupported,
            },
        }

    def render_markdown(self) -> str:
        """One line for the sticky comment, marked as informational."""
        if self.monthly is None:
            amount = "no monthly cost reported"
        else:
            amount = f"{format_money(self.monthly, self.currency)}/month"
            if self.diff_monthly is not None and self.past_monthly is not None:
                amount += f" ({format_money(self.diff_monthly, self.currency, signed=True)} vs. the current state)"
        scope = f"{self.supported} of {self.detected} resources priced"
        if self.usage_based:
            scope += f"; {self.usage_based} usage-based, priced at zero usage"
        if self.unsupported:
            scope += f"; {self.unsupported} not supported by Infracost"
        return f"{COST_LINE_PREFIX} {amount}. {scope}."


def _str(value: Decimal | None) -> str | None:
    return None if value is None else str(value)


def _decimal(value: Any) -> Decimal | None:
    """Infracost amounts are decimal strings (or null). Anything else is None."""
    if value is None or isinstance(value, bool):
        return None
    try:
        amount = Decimal(str(value))
    except InvalidOperation:
        return None
    return amount if amount.is_finite() else None


def _count(summary: Mapping[str, Any], key: str) -> int:
    value = summary.get(key)
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0


def parse_infracost(doc: Any) -> CostEstimate | None:
    """``infracost breakdown --format json`` output as a CostEstimate, or None if it is not one."""
    if not isinstance(doc, Mapping) or "totalMonthlyCost" not in doc or not isinstance(doc.get("projects"), list):
        return None
    summary = doc.get("summary") if isinstance(doc.get("summary"), Mapping) else {}
    currency = doc.get("currency")
    return CostEstimate(
        currency=currency if isinstance(currency, str) and currency.isalpha() else "USD",
        monthly=_decimal(doc.get("totalMonthlyCost")),
        past_monthly=_decimal(doc.get("pastTotalMonthlyCost")),
        diff_monthly=_decimal(doc.get("diffTotalMonthlyCost")),
        detected=_count(summary, "totalDetectedResources"),
        supported=_count(summary, "totalSupportedResources"),
        usage_based=_count(summary, "totalUsageBasedResources"),
        unsupported=_count(summary, "totalUnsupportedResources"),
    )


def format_money(amount: Decimal, currency: str, signed: bool = False) -> str:
    """``Decimal("1234.5"), "USD"`` -> ``$1,234.50``; other currencies use their code."""
    rounded = amount.quantize(_CENT, rounding=ROUND_HALF_UP)
    sign = "-" if rounded < 0 else "+" if signed and rounded > 0 else ""
    digits = f"{abs(rounded):,.2f}"
    symbol = _SYMBOLS.get(currency)
    return f"{sign}{symbol}{digits}" if symbol else f"{sign}{digits} {currency}"


# ---- the plan handed to the cost job -----------------------------------------------


def _mask(value: Any, sensitive: Any) -> Any:
    """``value`` with every part ``sensitive`` marks true replaced by None. Pure.

    ``sensitive`` mirrors the value's shape (Terraform's ``after_sensitive`` and
    ``sensitive_values``): true for a sensitive leaf, an object or list for a
    structure with sensitive parts, false or missing for nothing sensitive.
    """
    if sensitive is True:
        return None
    if isinstance(value, Mapping) and isinstance(sensitive, Mapping):
        return {k: _mask(v, sensitive.get(k)) for k, v in value.items()}
    if isinstance(value, list) and isinstance(sensitive, list):
        return [_mask(v, sensitive[i] if i < len(sensitive) else None) for i, v in enumerate(value)]
    return copy.deepcopy(value)


def _redact_module(module: Mapping[str, Any]) -> dict[str, Any]:
    resources = [
        {**r, "values": _mask(r.get("values"), r.get("sensitive_values"))} if isinstance(r, Mapping) else r
        for r in module.get("resources") or []
    ]
    children = [_redact_module(c) for c in module.get("child_modules") or [] if isinstance(c, Mapping)]
    return {**module, "resources": resources, "child_modules": children}


def _redact_change(rc: Any) -> Any:
    change = rc.get("change") if isinstance(rc, Mapping) else None
    if not isinstance(change, Mapping):
        return rc
    return {
        **rc,
        "change": {
            **change,
            "before": _mask(change.get("before"), change.get("before_sensitive")),
            "after": _mask(change.get("after"), change.get("after_sensitive")),
        },
    }


def redact_plan(plan: Mapping[str, Any]) -> dict[str, Any]:
    """The plan JSON for Infracost: sensitive values nulled, unpriced parts dropped. Pure.

    Values that are only known after apply are not in the plan at all, and module
    outputs and variables are dropped, so what is left is the configuration and
    the non-sensitive planned attributes Infracost prices.
    """
    out = {k: copy.deepcopy(v) for k, v in plan.items() if k not in _DROPPED_PLAN_KEYS}
    if isinstance(plan.get("resource_changes"), list):
        out["resource_changes"] = [_redact_change(rc) for rc in plan["resource_changes"]]
    planned = plan.get("planned_values")
    if isinstance(planned, Mapping):
        root = planned.get("root_module")
        out["planned_values"] = {
            **{k: copy.deepcopy(v) for k, v in planned.items() if k not in ("outputs", "root_module")},
            **({"root_module": _redact_module(root)} if isinstance(root, Mapping) else {}),
        }
    return out


# ---- folding a later estimate into a rendered summary ------------------------------


def _without_cost_line(lines: list[str]) -> list[str]:
    """``lines`` minus a previous cost line and the blank line before it. Pure."""
    at = next((i for i, line in enumerate(lines) if line.startswith(COST_LINE_PREFIX)), None)
    if at is None:
        return lines
    start = at - 1 if at > 0 and lines[at - 1] == "" else at
    return lines[:start] + lines[at + 1:]


def fold_cost(summary: Mapping[str, Any], markdown: str, estimate: CostEstimate) -> tuple[dict[str, Any], str]:
    """``summary.json`` and ``comment.md`` with ``estimate`` added. Pure and idempotent.

    The cost line goes right after the ``**Plan:**`` line, where ``render_markdown``
    puts it, or before the footer (``<sub>``) when there is no plan line, or at the
    end. A previous cost line is replaced. ``ok`` is never touched.
    """
    lines = _without_cost_line(markdown.rstrip("\n").split("\n"))
    plan_at = next((i for i, line in enumerate(lines) if line.startswith("**Plan:**")), None)
    footer_at = next((i for i, line in enumerate(lines) if line.startswith("<sub>")), None)
    if plan_at is not None:
        at = plan_at + 1
    elif footer_at is not None:
        at = footer_at - 1 if footer_at > 0 and lines[footer_at - 1] == "" else footer_at
    else:
        at = len(lines)
    folded_lines = lines[:at] + ["", estimate.render_markdown()] + lines[at:]
    return {**summary, "cost": estimate.to_dict()}, "\n".join(folded_lines) + "\n"
