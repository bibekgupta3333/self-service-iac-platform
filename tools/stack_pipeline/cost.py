"""Infracost cost estimate for the plan summary (WBS 5.5, informational in the MVP).

Everything here is pure: ``infracost breakdown --format json`` output goes in, a
``CostEstimate`` value and its one-line Markdown rendering come out. A cost never
changes whether a summary is ``ok``; budget gating is WBS 8.5.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal, InvalidOperation
from typing import Any, Mapping

_SYMBOLS = {"USD": "$", "EUR": "€", "GBP": "£"}
_CENT = Decimal("0.01")


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
        return f"**Estimated cost (Infracost, informational):** {amount}. {scope}."


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
