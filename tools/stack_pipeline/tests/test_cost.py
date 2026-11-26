import json
from decimal import Decimal

import pytest

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.cost import CostEstimate, format_money, parse_infracost
from tools.stack_pipeline.plan_summary import render_markdown, summarize

# Shaped like `infracost breakdown --path plan.json --format json` (Infracost 0.10).
INFRACOST = {
    "version": "0.2",
    "currency": "USD",
    "projects": [{"name": "environments/development/platform/smoke", "breakdown": {"totalMonthlyCost": "1.0004"}}],
    "totalHourlyCost": "0.00137",
    "totalMonthlyCost": "1.0004",
    "pastTotalMonthlyCost": None,
    "diffTotalMonthlyCost": None,
    "summary": {
        "totalDetectedResources": 3,
        "totalSupportedResources": 2,
        "totalUsageBasedResources": 1,
        "totalUnsupportedResources": 0,
        "totalNoPriceResources": 1,
    },
}

PLAN = {"resource_changes": [{"address": "aws_kms_key.k", "mode": "managed", "change": {"actions": ["create"]}}]}


class TestParse:
    def test_breakdown(self):
        est = parse_infracost(INFRACOST)
        assert est == CostEstimate(
            currency="USD", monthly=Decimal("1.0004"), detected=3, supported=2, usage_based=1, unsupported=0
        )

    def test_diff(self):
        est = parse_infracost(dict(INFRACOST, pastTotalMonthlyCost="3", diffTotalMonthlyCost="-1.9996"))
        assert est.past_monthly == Decimal("3") and est.diff_monthly == Decimal("-1.9996")

    @pytest.mark.parametrize("doc", [None, [], "x", {"projects": []}, {"totalMonthlyCost": "1"}])
    def test_not_infracost_output(self, doc):
        assert parse_infracost(doc) is None

    @pytest.mark.parametrize("value", [None, "abc", True, "NaN", "Infinity", {}])
    def test_bad_amounts_become_none(self, value):
        assert parse_infracost(dict(INFRACOST, totalMonthlyCost=value)).monthly is None

    def test_bad_summary_and_currency(self):
        doc = dict(INFRACOST, summary={"totalDetectedResources": -1, "totalSupportedResources": "2"}, currency="<b>")
        est = parse_infracost(doc)
        assert (est.detected, est.supported, est.currency) == (0, 0, "USD")


class TestFormat:
    @pytest.mark.parametrize(
        "amount,currency,signed,expected",
        [
            ("1.0004", "USD", False, "$1.00"),
            ("1234.5", "USD", False, "$1,234.50"),
            ("0.005", "USD", False, "$0.01"),
            ("2", "USD", True, "+$2.00"),
            ("-2", "USD", True, "-$2.00"),
            ("0", "USD", True, "$0.00"),
            ("10", "EUR", False, "€10.00"),
            ("10", "CHF", False, "10.00 CHF"),
        ],
    )
    def test_format_money(self, amount, currency, signed, expected):
        assert format_money(Decimal(amount), currency, signed) == expected

    def test_line_without_diff(self):
        assert parse_infracost(INFRACOST).render_markdown() == (
            "**Estimated cost (Infracost, informational):** $1.00/month. "
            "2 of 3 resources priced; 1 usage-based, priced at zero usage."
        )

    def test_line_with_diff_and_unsupported(self):
        summary = {"totalDetectedResources": 2, "totalSupportedResources": 1, "totalUnsupportedResources": 1}
        doc = dict(INFRACOST, pastTotalMonthlyCost="0", diffTotalMonthlyCost="1.0004", summary=summary)
        line = parse_infracost(doc).render_markdown()
        assert "$1.00/month (+$1.00 vs. the current state)" in line
        assert "1 of 2 resources priced; 1 not supported by Infracost." in line

    def test_line_without_total(self):
        assert "no monthly cost reported" in CostEstimate("USD", None).render_markdown()

    def test_to_dict_keeps_precision(self):
        assert parse_infracost(INFRACOST).to_dict() == {
            "currency": "USD",
            "monthly": "1.0004",
            "past_monthly": None,
            "diff_monthly": None,
            "resources": {"detected": 3, "supported": 2, "usage_based": 1, "unsupported": 0},
        }


class TestSummaryIntegration:
    def test_cost_never_changes_ok(self):
        steps = {"plan": "success"}
        without = summarize("id", "p", PLAN, [{"failures": []}], steps)
        with_cost = summarize("id", "p", PLAN, [{"failures": []}], steps, cost=parse_infracost(INFRACOST))
        assert without.ok and with_cost.ok
        assert without.to_dict()["cost"] is None
        assert with_cost.to_dict()["cost"]["monthly"] == "1.0004"

    def test_markdown_shows_cost_only_when_present(self):
        with_cost = summarize("id", "p", PLAN, [{"failures": []}], {}, cost=parse_infracost(INFRACOST))
        assert "**Estimated cost (Infracost, informational):** $1.00/month" in render_markdown(with_cost)
        assert "Estimated cost" not in render_markdown(summarize("id", "p", PLAN, [{"failures": []}], {}))

    def test_summary_command_with_cost(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke")
        (tmp_path / "plan.json").write_text(json.dumps(PLAN))
        (tmp_path / "conftest.json").write_text("[]")
        (tmp_path / "infracost.json").write_text(json.dumps(INFRACOST))
        md, js = tmp_path / "c.md", tmp_path / "s.json"
        base = ["--repo-root", str(tmp_path), "summary", str(root), "--plan", str(tmp_path / "plan.json"),
                "--conftest", str(tmp_path / "conftest.json"), "--markdown-out", str(md), "--json-out", str(js)]
        assert main([*base, "--cost", str(tmp_path / "infracost.json")]) == 0
        assert json.loads(js.read_text())["cost"]["currency"] == "USD"
        assert "$1.00/month" in md.read_text()
        # A skipped Infracost step leaves no file: no cost line, and the summary still passes.
        assert main([*base, "--cost", str(tmp_path / "missing.json")]) == 0
        data = json.loads(js.read_text())
        assert data["cost"] is None and data["ok"] is True
        assert "Estimated cost" not in md.read_text()
