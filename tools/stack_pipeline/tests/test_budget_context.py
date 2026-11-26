"""The budget gate's context: pure builders, the ``context --budget-gate`` command and the
API fetch (policies/opa/budget.rego, docs/07 section 6.7)."""

import json
from decimal import Decimal

import pytest

from tools.stack_pipeline.api_client import ApiError, Credential, IacpApiClient
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.cost import CostEstimate, parse_infracost
from tools.stack_pipeline.models import StackRef
from tools.stack_pipeline.policy_context import (
    ContextError,
    build_budget_context,
    estimated_monthly_usd,
    merge_api_context,
    with_budget_gate,
)

from .test_api_client import INTERNAL, api  # noqa: F401  (the loopback API fixture)
from .test_cost import INFRACOST

REF = StackRef("staging", "payments", "orders")
PATH = REF.path

API_CONTEXT = {
    "iacp": {
        "context": {
            "environment": "staging",
            "team": "payments",
            "stack": "orders",
            "stack_path": PATH,
            "allowed_regions": ["us-east-1"],
            "budget": {"month": "2026-11", "limit_usd": 2000.0, "spent_usd": 1500.0, "actual_usd": None,
                       "hard_block": True, "scope": "team"},
            "budget_override": None,
            "exceptions": [],
            "yanked_module_versions": [],
            "now": "2026-11-24T10:05:00Z",
        }
    }
}


def est(monthly, currency="USD"):
    return CostEstimate(currency=currency, monthly=None if monthly is None else Decimal(str(monthly)))


def ctx(document):
    return document["iacp"]["context"]


class TestEstimatedMonthlyUsd:
    @pytest.mark.parametrize("monthly,want", [("600", 600), ("600.00", 600), ("1.0004", 1.0004), ("0", 0), ("12.5", 12.5)])
    def test_a_usable_total_is_a_plain_number(self, monthly, want):
        got = estimated_monthly_usd(est(monthly))
        assert got == want and isinstance(got, (int, float)) and not isinstance(got, bool)

    @pytest.mark.parametrize("estimate", [None, est(None), est("-1"), est("100", "EUR")])
    def test_anything_else_is_missing(self, estimate):
        assert estimated_monthly_usd(estimate) is None

    def test_json_round_trips_as_a_number(self):
        assert json.loads(json.dumps({"v": estimated_monthly_usd(est("600"))})) == {"v": 600}


class TestWithBudgetGate:
    def test_sets_the_gate_and_the_estimate(self):
        out = with_budget_gate({"iacp": {"context": {"team": "payments"}}}, est("600"))
        assert ctx(out) == {"team": "payments", "budget_gate": True, "estimated_monthly_cost_usd": 600}

    def test_without_an_estimate_the_key_is_absent_not_zero(self):
        out = with_budget_gate({"iacp": {"context": {}}}, None)
        assert ctx(out) == {"budget_gate": True}

    def test_an_estimate_already_in_the_document_is_discarded(self):
        """Only the cost job's number counts: a forged value in the API document or an earlier
        step cannot survive, whether or not a real estimate follows."""
        forged = {"iacp": {"context": {"estimated_monthly_cost_usd": 0, "budget_gate": False}}}
        assert "estimated_monthly_cost_usd" not in ctx(with_budget_gate(forged, None))
        assert ctx(with_budget_gate(forged, est("700")))["estimated_monthly_cost_usd"] == 700
        assert ctx(with_budget_gate(forged, None))["budget_gate"] is True

    def test_is_idempotent_and_does_not_mutate_its_input(self):
        doc = {"iacp": {"context": {"team": "payments"}}}
        once = with_budget_gate(doc, est("1"))
        assert with_budget_gate(once, est("1")) == once
        assert doc == {"iacp": {"context": {"team": "payments"}}}

    def test_a_malformed_document_still_yields_a_gate(self):
        assert ctx(with_budget_gate({}, est("5"))) == {"budget_gate": True, "estimated_monthly_cost_usd": 5}
        assert ctx(with_budget_gate({"iacp": []}, None)) == {"budget_gate": True}


class TestMergeApiContext:
    def base(self):
        return {"iacp": {"context": {"environment": "staging", "team": "payments", "stack": "orders",
                                     "stack_path": PATH, "exceptions": []}}}

    def test_takes_the_budget_and_the_clock_from_the_api(self):
        out = ctx(merge_api_context(REF, self.base(), API_CONTEXT))
        assert out["budget"]["limit_usd"] == 2000.0
        assert out["now"] == "2026-11-24T10:05:00Z" and out["allowed_regions"] == ["us-east-1"]
        assert out["stack_path"] == PATH

    @pytest.mark.parametrize("key,value", [("environment", "production"), ("team", "search"), ("stack", "other")])
    def test_a_document_for_another_stack_is_refused(self, key, value):
        doc = json.loads(json.dumps(API_CONTEXT))
        ctx(doc)[key] = value
        with pytest.raises(ContextError, match=key):
            merge_api_context(REF, self.base(), doc)

    def test_a_document_for_another_path_is_refused(self):
        doc = json.loads(json.dumps(API_CONTEXT))
        ctx(doc)["stack_path"] = "environments/staging/payments/elsewhere"
        with pytest.raises(ContextError, match="stack_path|elsewhere"):
            merge_api_context(REF, self.base(), doc)

    @pytest.mark.parametrize("doc", [{}, {"iacp": None}, {"iacp": {"context": []}}])
    def test_a_document_without_a_context_is_refused(self, doc):
        with pytest.raises(ContextError, match="no iacp.context"):
            merge_api_context(REF, self.base(), doc)

    def test_keys_the_api_does_not_own_are_ignored(self):
        doc = json.loads(json.dumps(API_CONTEXT))
        ctx(doc).update({"estimated_monthly_cost_usd": 0, "budget_gate": False, "environment_override": "x"})
        out = ctx(merge_api_context(REF, self.base(), doc))
        assert not {"estimated_monthly_cost_usd", "budget_gate", "environment_override"} & out.keys()


class TestBuildBudgetContext:
    def test_the_gate_context(self):
        out = ctx(build_budget_context(REF, est("600"), API_CONTEXT))
        assert out["environment"] == "staging" and out["budget"]["spent_usd"] == 1500.0
        assert out["budget_gate"] is True and out["estimated_monthly_cost_usd"] == 600

    def test_without_the_api_there_is_no_budget(self):
        out = ctx(build_budget_context(REF, est("600")))
        assert "budget" not in out and out["budget_gate"] is True
        assert (out["team"], out["stack_path"], out["exceptions"]) == ("payments", PATH, [])

    def test_a_forged_estimate_in_the_api_document_does_not_reach_the_policy(self):
        doc = json.loads(json.dumps(API_CONTEXT))
        ctx(doc)["estimated_monthly_cost_usd"] = 0
        assert "estimated_monthly_cost_usd" not in ctx(build_budget_context(REF, None, doc))

    def test_allowed_regions_override(self):
        assert ctx(build_budget_context(REF, None, allowed_regions=["eu-west-1"]))["allowed_regions"] == ["eu-west-1"]


class TestStackRefFromRoot:
    @pytest.mark.parametrize("path", [PATH, PATH + "/", "/" + PATH])
    def test_a_root(self, path):
        assert StackRef.from_root(path) == REF

    @pytest.mark.parametrize("path", ["", PATH + "/main.tf", "environments/staging/payments", "environments/qa/payments/orders",
                                      "environments/staging/Payments/orders", "../" + PATH, PATH + "/../x"])
    def test_not_a_root(self, path):
        assert StackRef.from_root(path) is None


def run(capsys, *argv):
    rc = main(["context", "--budget-gate", *argv])
    captured = capsys.readouterr()
    return rc, captured.out, captured.err


class TestContextCommand:
    def write(self, tmp_path, name, doc):
        path = tmp_path / name
        path.write_text(json.dumps(doc), encoding="utf-8")
        return str(path)

    def test_gate_context_from_the_files(self, tmp_path, capsys):
        cost = self.write(tmp_path, "infracost.json", dict(INFRACOST, totalMonthlyCost="600"))
        api_ctx = self.write(tmp_path, "api.json", API_CONTEXT)
        rc, out, err = run(capsys, "--stack-path", PATH, "--cost", cost, "--api-context", api_ctx)
        assert rc == 0
        got = ctx(json.loads(out))
        assert got["estimated_monthly_cost_usd"] == 600 and got["budget"]["limit_usd"] == 2000.0 and got["budget_gate"] is True
        assert "estimate 600" in err and "budget set" in err

    def test_a_missing_cost_file_means_no_estimate(self, tmp_path, capsys):
        rc, out, err = run(capsys, "--stack-path", PATH, "--cost", str(tmp_path / "absent.json"))
        got = ctx(json.loads(out))
        assert rc == 0 and "estimated_monthly_cost_usd" not in got and got["budget_gate"] is True
        assert "estimate missing" in err and "budget not set" in err

    @pytest.mark.parametrize("content", ["", "not json", "[]", '{"totalMonthlyCost": "1"}'])
    def test_an_unusable_cost_file_means_no_estimate(self, tmp_path, capsys, content):
        path = tmp_path / "infracost.json"
        path.write_text(content, encoding="utf-8")
        rc, out, _ = run(capsys, "--stack-path", PATH, "--cost", str(path))
        assert rc == 0 and "estimated_monthly_cost_usd" not in ctx(json.loads(out))

    def test_a_non_usd_estimate_is_missing(self, tmp_path, capsys):
        cost = self.write(tmp_path, "infracost.json", dict(INFRACOST, currency="EUR", totalMonthlyCost="600"))
        _, out, _ = run(capsys, "--stack-path", PATH, "--cost", cost)
        assert "estimated_monthly_cost_usd" not in ctx(json.loads(out))

    def test_an_absent_api_context_means_no_budget(self, tmp_path, capsys):
        rc, out, _ = run(capsys, "--stack-path", PATH, "--api-context", str(tmp_path / "absent.json"))
        assert rc == 0 and "budget" not in ctx(json.loads(out))

    def test_a_garbled_api_context_fails_closed(self, tmp_path, capsys):
        bad = tmp_path / "api.json"
        bad.write_text("<html>", encoding="utf-8")
        rc, out, _ = run(capsys, "--stack-path", PATH, "--api-context", str(bad))
        assert rc == 1 and out == ""

    def test_an_api_context_for_another_stack_fails_closed(self, tmp_path, capsys):
        other = json.loads(json.dumps(API_CONTEXT))
        ctx(other)["team"] = "search"
        rc, _, _ = run(capsys, "--stack-path", PATH, "--api-context", self.write(tmp_path, "api.json", other))
        assert rc == 1

    @pytest.mark.parametrize("path", ["", "environments/staging/payments", PATH + "/main.tf", "../" + PATH])
    def test_needs_a_stack_root_path(self, capsys, path):
        assert main(["context", "--budget-gate", "--stack-path", path]) == 2
        assert "--stack-path" in capsys.readouterr().err

    def test_does_not_read_the_stack_directory(self, tmp_path, make_stack, capsys):
        """The identity is the path: a stack directory is refused rather than read."""
        root = make_stack("staging", "payments", "orders")
        assert main(["--repo-root", str(tmp_path), "context", "--budget-gate", "--stack-path", PATH, str(root)]) == 2

    def test_cost_and_api_flags_need_the_gate(self, tmp_path, make_stack, capsys):
        root = make_stack("staging", "payments", "orders")
        assert main(["--repo-root", str(tmp_path), "context", str(root), "--cost", "x.json"]) == 2
        assert main(["context", "--stack-path", PATH]) == 2

    def test_the_plain_context_is_unchanged(self, tmp_path, make_stack, capsys):
        root = make_stack("staging", "payments", "orders")
        assert main(["--repo-root", str(tmp_path), "context", str(root)]) == 0
        assert ctx(json.loads(capsys.readouterr().out)) == {
            "environment": "staging", "team": "payments", "stack": "orders", "exceptions": []}

    def test_the_estimate_matches_the_cost_summary_total(self):
        """context and the PR comment read the same Infracost total."""
        doc = dict(INFRACOST, totalMonthlyCost="600")
        assert estimated_monthly_usd(parse_infracost(doc)) == 600


class TestFetchBudgetContext:
    URL = "/v1/internal/budget-context"

    def client(self, api):
        return IacpApiClient(api.url, Credential(INTERNAL, "internal"), sleep=lambda _: None)

    def test_gets_the_document(self, api):
        api.reply("GET", self.URL, 200, API_CONTEXT)
        assert self.client(api).get_budget_context(PATH) == API_CONTEXT
        assert api.calls[0]["query"] == {"stack_path": [PATH]}
        assert api.calls[0]["headers"]["Authorization"] == f"Bearer {INTERNAL}"

    def test_retries_a_transient_error(self, api):
        api.reply("GET", self.URL, 503, {"code": "unavailable"})
        api.reply("GET", self.URL, 200, API_CONTEXT)
        assert self.client(api).get_budget_context(PATH) == API_CONTEXT and len(api.calls) == 2

    @pytest.mark.parametrize("body", [{}, {"iacp": {}}, {"iacp": {"context": "x"}}])
    def test_an_answer_that_is_not_a_context_is_an_error(self, api, body):
        api.reply("GET", self.URL, 200, body)
        with pytest.raises(ApiError, match="not a policy context"):
            self.client(api).get_budget_context(PATH)

    def test_rejects_a_bad_path_before_calling(self, api):
        with pytest.raises(ValueError, match="stack path"):
            self.client(api).get_budget_context("environments/staging/payments")
        assert api.calls == []

    def test_a_refusal_is_an_error(self, api):
        api.reply("GET", self.URL, 403, {"code": "workflow_not_allowed"})
        with pytest.raises(ApiError, match="403"):
            self.client(api).get_budget_context(PATH)

    def test_command_writes_the_file(self, tmp_path, api, monkeypatch):
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        for k in ("ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"):
            monkeypatch.delenv(k, raising=False)
        api.reply("GET", self.URL, 200, API_CONTEXT)
        out = tmp_path / "api.json"
        assert main(["fetch-budget-context", "--stack-path", PATH, "--out", str(out), "--api-url", api.url]) == 0
        assert json.loads(out.read_text()) == API_CONTEXT

    def test_command_skips_when_the_api_is_not_configured(self, tmp_path, monkeypatch, capsys):
        monkeypatch.delenv("IACP_API_URL", raising=False)
        out = tmp_path / "api.json"
        assert main(["fetch-budget-context", "--stack-path", PATH, "--out", str(out), "--skip-if-unconfigured"]) == 0
        assert not out.exists() and "local-first" in capsys.readouterr().out

    def test_command_fails_closed_when_the_api_is_configured_but_down(self, tmp_path, api, monkeypatch, capsys):
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        for k in ("ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"):
            monkeypatch.delenv(k, raising=False)
        api.reply("GET", self.URL, 500, {"code": "internal"})
        out = tmp_path / "api.json"
        rc = main(["fetch-budget-context", "--stack-path", PATH, "--out", str(out), "--api-url", api.url])
        assert rc == 1 and not out.exists()
