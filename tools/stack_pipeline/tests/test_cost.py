import json
from decimal import Decimal

import pytest

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.cost import CostEstimate, fold_cost, format_money, parse_infracost, redact_plan
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


SECRET_PLAN = {
    "format_version": "1.2",
    "variables": {"db_password": {"value": "hunter2"}, "region": {"value": "eu-west-1"}},
    "prior_state": {"values": {"root_module": {"resources": [{"values": {"password": "old-secret"}}]}}},
    "output_changes": {"conn": {"after": "postgres://u:hunter2@h"}},
    "resource_drift": [{"address": "aws_db_instance.db", "change": {"before": {"password": "drift-secret"}}}],
    "relevant_attributes": [{"resource": "aws_db_instance.db", "attribute": ["password"]}],
    "checks": [{"address": {"kind": "var", "name": "x"}, "status": "pass"}],
    "some_future_key": {"token": "tok-123"},
    "configuration": {
        "provider_config": {"aws": {"name": "aws", "expressions": {
            "region": {"references": ["var.region", "var"]},
            "access_key": {"constant_value": "AKIAKEY"},
            "default_tags": [{"tags": {"constant_value": {"a": "b"}}}]}}},
        "root_module": {
            "resources": [{"address": "aws_db_instance.db", "type": "aws_db_instance", "provider_config_key": "aws",
                           "expressions": {"password": {"constant_value": "cfg-secret"},
                                           "identifier": {"references": ["var.name", "var"]}}}],
            "variables": {"db_password": {"default": "var-default-secret", "sensitive": True}},
            "module_calls": {"m": {"source": "./m", "expressions": {"p": {"constant_value": "call-secret"}},
                                   "module": {"variables": {"x": {"default": "nested-secret"}}, "resources": []}}},
            "outputs": {"conn": {"expression": {"constant_value": "out-secret"}}},
        },
    },
    "planned_values": {
        "outputs": {"conn": {"sensitive": True, "value": "postgres://u:hunter2@h"}},
        "root_module": {
            "resources": [
                {"address": "aws_db_instance.db", "type": "aws_db_instance",
                 "values": {"instance_class": "db.t3.micro", "password": "hunter2", "tags": {"a": "b"}},
                 "sensitive_values": {"password": True, "tags": {}}},
            ],
            "child_modules": [
                {"address": "module.m", "resources": [
                    {"address": "module.m.aws_secretsmanager_secret_version.v", "type": "aws_secretsmanager_secret_version",
                     "values": {"secret_string": "s3cr3t", "list": ["a", "b"]},
                     "sensitive_values": {"secret_string": True, "list": [False, True]}},
                ]},
            ],
        },
    },
    "resource_changes": [
        {"address": "aws_db_instance.db", "mode": "managed", "type": "aws_db_instance",
         "change": {"actions": ["update"],
                    "before": {"instance_class": "db.t3.micro", "password": "old-secret"},
                    "after": {"instance_class": "db.t3.small", "password": "hunter2", "nested": {"k": "v"}},
                    "before_sensitive": {"password": True},
                    "after_sensitive": {"password": True, "nested": True}}},
    ],
}


class TestRedactPlan:
    def test_no_sensitive_value_survives(self):
        out = json.dumps(redact_plan(SECRET_PLAN))
        for secret in ("hunter2", "old-secret", "s3cr3t", '"v"', "drift-secret", "tok-123", "AKIAKEY",
                       "cfg-secret", "var-default-secret", "call-secret", "nested-secret", "out-secret"):
            assert secret not in out

    def test_keeps_what_infracost_prices(self):
        out = redact_plan(SECRET_PLAN)
        assert set(out) == {"format_version", "configuration", "planned_values", "resource_changes", "variables"}
        db = out["planned_values"]["root_module"]["resources"][0]["values"]
        assert db == {"instance_class": "db.t3.micro", "password": None, "tags": {"a": "b"}}
        child = out["planned_values"]["root_module"]["child_modules"][0]["resources"][0]["values"]
        assert child == {"secret_string": None, "list": ["a", None]}
        change = out["resource_changes"][0]["change"]
        assert change["after"] == {"instance_class": "db.t3.small", "password": None, "nested": None}
        assert change["before"] == {"instance_class": "db.t3.micro", "password": None}
        assert change["actions"] == ["update"]
        assert "outputs" not in out["planned_values"]

    def test_unknown_top_level_keys_are_dropped(self):
        out = redact_plan(SECRET_PLAN)
        for key in ("resource_drift", "relevant_attributes", "checks", "some_future_key", "prior_state",
                    "output_changes"):
            assert key not in out
        assert set(redact_plan({"anything": 1, "terraform_version": "1.13.5"})) == {"terraform_version"}

    def test_keeps_the_fields_infracost_reads(self):
        out = redact_plan(SECRET_PLAN)
        rc = out["resource_changes"][0]
        assert (rc["address"], rc["type"], rc["mode"]) == ("aws_db_instance.db", "aws_db_instance", "managed")
        res = out["planned_values"]["root_module"]["resources"][0]
        assert (res["address"], res["type"]) == ("aws_db_instance.db", "aws_db_instance")
        cfg = out["configuration"]
        assert cfg["provider_config"]["aws"]["expressions"] == {
            "region": {"references": ["var.region", "var"]},
            "default_tags": [{"tags": {"constant_value": {"a": "b"}}}],
        }
        assert cfg["root_module"]["resources"][0]["expressions"] == {"identifier": {"references": ["var.name", "var"]}}
        assert "variables" not in cfg["root_module"] and "outputs" not in cfg["root_module"]
        assert cfg["root_module"]["module_calls"] == {"m": {"source": "./m", "module": {"resources": []}}}
        # Only the variable the provider region refers to is kept.
        assert out["variables"] == {"region": {"value": "eu-west-1"}}

    def test_does_not_mutate_and_tolerates_odd_shapes(self):
        before = json.dumps(SECRET_PLAN, sort_keys=True)
        redact_plan(SECRET_PLAN)
        assert json.dumps(SECRET_PLAN, sort_keys=True) == before
        assert redact_plan({}) == {}
        odd = {"resource_changes": ["x", {"change": None}], "planned_values": {"root_module": None}}
        assert redact_plan(odd) == {
            "resource_changes": [{"change": {"before": None, "after": None}}], "planned_values": {}}


SUMMARY_MD = """<!-- iacp-plan:id -->
### pr-validate: `p` (passed)

| Step | Result |
|------|--------|
| plan | success |

**Plan:** 1 to add, 0 to change, 0 to replace, 0 to destroy. Plan hash `abc`.

<details><summary>terraform plan output</summary>

```text
a


b
```

</details>

<sub>commit `0123`</sub>
"""


class TestFoldCost:
    def test_folds_after_the_plan_line_and_keeps_ok(self):
        estimate = parse_infracost(INFRACOST)
        summary, md = fold_cost({"ok": False, "cost": None}, SUMMARY_MD, estimate)
        assert summary == {"ok": False, "cost": estimate.to_dict()}
        assert "Plan hash `abc`.\n\n**Estimated cost (Infracost, informational):** $1.00/month." in md
        assert "a\n\n\nb" in md  # blank lines inside the plan output are untouched

    def test_idempotent_and_replaces_an_old_estimate(self):
        first = fold_cost({}, SUMMARY_MD, parse_infracost(INFRACOST))[1]
        again = fold_cost({}, first, parse_infracost(INFRACOST))[1]
        assert again == first
        cheaper = fold_cost({}, first, parse_infracost(dict(INFRACOST, totalMonthlyCost="0")))[1]
        assert cheaper.count("Estimated cost") == 1 and "$0.00/month" in cheaper
        assert fold_cost({}, render_markdown(summarize("id", "p", PLAN, [], {}, cost=parse_infracost(INFRACOST))),
                         parse_infracost(INFRACOST))[1].count("Estimated cost") == 1

    def test_without_a_plan_line(self):
        md = "### x\n\n**Plan:** not produced\n"
        assert fold_cost({}, "### x\n\n<sub>f</sub>\n", parse_infracost(INFRACOST))[1].endswith(
            "\n\n**Estimated cost (Infracost, informational):** $1.00/month. "
            "2 of 3 resources priced; 1 usage-based, priced at zero usage.\n\n<sub>f</sub>\n")
        assert fold_cost({}, "### x\n", parse_infracost(INFRACOST))[1].startswith("### x\n\n**Estimated cost")
        assert "not produced\n\n**Estimated" in fold_cost({}, md, parse_infracost(INFRACOST))[1]


class TestCostCommands:
    def test_cost_input(self, tmp_path):
        (tmp_path / "plan.json").write_text(json.dumps(SECRET_PLAN))
        assert main(["cost-input", "--plan", str(tmp_path / "plan.json"), "--out", str(tmp_path / "out.json")]) == 0
        assert "hunter2" not in (tmp_path / "out.json").read_text()
        assert main(["cost-input", "--plan", str(tmp_path / "nope.json"), "--out", str(tmp_path / "o.json")]) == 2

    def test_fold_cost_in_place_and_noop_without_estimate(self, tmp_path, capsys):
        js, md, cost = tmp_path / "summary.json", tmp_path / "comment.md", tmp_path / "infracost.json"
        js.write_text(json.dumps({"ok": True, "cost": None}))
        md.write_text(SUMMARY_MD)
        argv = ["fold-cost", "--summary-json", str(js), "--markdown", str(md), "--cost", str(cost)]
        assert main(argv) == 0  # no estimate: informational, nothing changes
        assert "No cost estimate" in capsys.readouterr().out
        assert md.read_text() == SUMMARY_MD
        cost.write_text(json.dumps(INFRACOST))
        assert main(argv) == 0
        assert json.loads(js.read_text()) == {"ok": True, "cost": parse_infracost(INFRACOST).to_dict()}
        assert "$1.00/month" in md.read_text()
