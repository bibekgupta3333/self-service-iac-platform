"""Infracost cost estimate for the plan summary (WBS 5.5, informational in the MVP).

Everything here is pure:

- ``redact_plan``: the plan JSON handed to the separate cost job, built from an
  allow-list of the fields Infracost reads, with every value Terraform marks
  sensitive replaced by null;
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
from typing import Any, Iterable, Mapping

_SYMBOLS = {"USD": "$", "EUR": "€", "GBP": "£"}
_CENT = Decimal("0.01")
COST_LINE_PREFIX = "**Estimated cost (Infracost, informational):**"
# The fields Infracost 0.10 reads from a Terraform plan JSON (internal/providers/terraform/parser.go);
# redact_plan keeps these and nothing else:
#   terraform_version, format_version
#   planned_values.root_module: child_modules[], resources[] (address, type, provider_name, index, values)
#   resource_changes[]: address, change.actions (stripNonTargetResources reads the address)
#   configuration.root_module: resources[] (address, provider_config_key, expressions.*.references),
#       module_calls (source, module)
#   configuration.provider_config.<key>: name, expressions.region, expressions.default_tags
#   variables.<name>.value: only for a variable the provider region refers to
# prior_state is left out on purpose: without it Infracost prices the plan alone (no "past" cost).
_PLAN_META_KEYS = ("format_version", "terraform_version")
_RESOURCE_ID_KEYS = ("address", "mode", "type", "name", "index", "provider_name")
_PROVIDER_EXPRESSIONS = ("region", "default_tags")


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


def _pick(source: Any, keys: Iterable[str]) -> dict[str, Any]:
    """The listed keys of ``source`` that it has, deep-copied. Pure."""
    if not isinstance(source, Mapping):
        return {}
    return {k: copy.deepcopy(source[k]) for k in keys if k in source}


def _redact_module(module: Mapping[str, Any]) -> dict[str, Any]:
    resources = [
        {**_pick(r, _RESOURCE_ID_KEYS), "values": _mask(r.get("values"), r.get("sensitive_values"))}
        for r in module.get("resources") or []
        if isinstance(r, Mapping)
    ]
    out: dict[str, Any] = {**_pick(module, ("address",)), "resources": resources}
    children = [_redact_module(c) for c in module.get("child_modules") or [] if isinstance(c, Mapping)]
    if children:
        out["child_modules"] = children
    return out


def _redact_change(rc: Any) -> dict[str, Any] | None:
    if not isinstance(rc, Mapping):
        return None
    change = rc.get("change") if isinstance(rc.get("change"), Mapping) else {}
    return {
        **_pick(rc, (*_RESOURCE_ID_KEYS, "module_address")),
        "change": {
            **_pick(change, ("actions",)),
            "before": _mask(change.get("before"), change.get("before_sensitive")),
            "after": _mask(change.get("after"), change.get("after_sensitive")),
        },
    }


def _references_only(expressions: Any) -> dict[str, Any]:
    """Each expression reduced to its ``references``: literals (``constant_value``) can be secrets. Pure."""
    if not isinstance(expressions, Mapping):
        return {}
    return {
        name: {"references": copy.deepcopy(expr["references"])}
        for name, expr in expressions.items()
        if isinstance(expr, Mapping) and isinstance(expr.get("references"), list)
    }


def _redact_config_module(module: Mapping[str, Any]) -> dict[str, Any]:
    calls = {
        name: {**_pick(call, ("source", "module_address")),
               **({"module": _redact_config_module(call["module"])} if isinstance(call.get("module"), Mapping) else {})}
        for name, call in (module.get("module_calls") or {}).items()
        if isinstance(call, Mapping)
    }
    resources = [
        {**_pick(r, ("address", "mode", "type", "name", "provider_config_key")),
         "expressions": _references_only(r.get("expressions"))}
        for r in module.get("resources") or []
        if isinstance(r, Mapping)
    ]
    return {"resources": resources, **({"module_calls": calls} if calls else {})}


def _redact_provider(provider: Any) -> dict[str, Any]:
    exprs = provider.get("expressions") if isinstance(provider, Mapping) else None
    kept = _pick(exprs, _PROVIDER_EXPRESSIONS)
    # default_tags carry literal tags (not secrets by contract); other attributes (keys, tokens) are dropped.
    return {**_pick(provider, ("name", "full_name", "module_address")), "expressions": kept}


def _region_variables(providers: Mapping[str, Any]) -> set[str]:
    names: set[str] = set()
    for provider in providers.values():
        region = ((provider.get("expressions") or {}).get("region") or {}) if isinstance(provider, Mapping) else {}
        refs = region.get("references") if isinstance(region, Mapping) else None
        names.update(r[4:] for r in refs or [] if isinstance(r, str) and r.startswith("var."))
    return names


def redact_plan(plan: Mapping[str, Any]) -> dict[str, Any]:
    """The plan JSON for Infracost, built from an allow-list. Pure.

    Only the fields listed above ``_PLAN_META_KEYS`` survive; every unknown
    key (``resource_drift``, ``prior_state``, ``output_changes``, ``relevant_attributes``,
    variable defaults in ``configuration``, ...) is dropped, and values Terraform marks
    sensitive are nulled. Values only known after apply are not in the plan at all.
    """
    out: dict[str, Any] = _pick(plan, _PLAN_META_KEYS)
    changes = plan.get("resource_changes")
    if isinstance(changes, list):
        out["resource_changes"] = [c for c in map(_redact_change, changes) if c is not None]
    planned = plan.get("planned_values")
    if isinstance(planned, Mapping):
        root = planned.get("root_module")
        out["planned_values"] = {"root_module": _redact_module(root)} if isinstance(root, Mapping) else {}
    config = plan.get("configuration")
    if isinstance(config, Mapping):
        providers = {
            k: _redact_provider(v) for k, v in (config.get("provider_config") or {}).items() if isinstance(v, Mapping)
        }
        root_cfg = config.get("root_module")
        out["configuration"] = {
            "provider_config": providers,
            **({"root_module": _redact_config_module(root_cfg)} if isinstance(root_cfg, Mapping) else {}),
        }
        wanted = _region_variables(providers)
        variables = plan.get("variables")
        if wanted and isinstance(variables, Mapping):
            out["variables"] = {k: {"value": copy.deepcopy(v.get("value"))} for k, v in variables.items()
                                if k in wanted and isinstance(v, Mapping)}
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
