import json

import pytest

from tools.stack_pipeline.cli import main, parse_steps
from tools.stack_pipeline.plan_summary import (
    PlanSummary,
    PolicyResult,
    classify,
    plan_hash,
    policy_result,
    render_markdown,
    resource_changes,
    summarize,
)
from tools.stack_pipeline.policy_context import build_context

from .conftest import metadata


def rc(address, actions, mode="managed", after=None):
    return {"address": address, "mode": mode, "change": {"actions": actions, "after": after or {}}}


PLAN = {
    "timestamp": "2026-11-25T00:00:00Z",
    "resource_changes": [
        rc("module.b.aws_s3_bucket.this", ["create"]),
        rc("module.b.data.aws_iam_policy_document.read", ["read"], mode="data"),
        rc("module.b.aws_kms_key.this[0]", ["create"]),
        rc("aws_sqs_queue.q", ["update"]),
        rc("aws_sqs_queue.r", ["delete", "create"]),
        rc("aws_sqs_queue.old", ["delete"]),
        rc("aws_sqs_queue.same", ["no-op"]),
    ],
}

CONFTEST = [
    {
        "filename": "plan.json",
        "namespace": "main",
        "successes": 2,
        "failures": [{"msg": "[iacp.tags.required] x: missing tag"}],
        "warnings": [{"msg": "[iacp.s3.policy_unknown] y: unknown"}],
    }
]

STEPS = {"fmt": "success", "validate": "success", "plan": "success"}


class TestPolicyContext:
    def test_context_from_metadata(self):
        ctx = build_context(metadata("development", "platform", "smoke"))
        assert ctx == {"iacp": {"context": {"environment": "development", "team": "platform", "stack": "smoke", "exceptions": []}}}

    def test_allowed_regions_override(self):
        ctx = build_context(metadata("staging", "payments", "orders"), ["eu-west-1"])
        assert ctx["iacp"]["context"]["allowed_regions"] == ["eu-west-1"]


class TestParsing:
    @pytest.mark.parametrize(
        "actions,expected",
        [
            (["create"], "create"),
            (["update"], "update"),
            (["delete"], "delete"),
            (["delete", "create"], "replace"),
            (["create", "delete"], "replace"),
            (["read"], "read"),
            (["no-op"], "no-op"),
            ([], "no-op"),
        ],
    )
    def test_classify(self, actions, expected):
        assert classify(actions) == expected

    def test_resource_changes_skip_reads_and_noops(self):
        changes = resource_changes(PLAN)
        assert [(c.address, c.action) for c in changes] == [
            ("aws_sqs_queue.old", "delete"),
            ("aws_sqs_queue.q", "update"),
            ("aws_sqs_queue.r", "replace"),
            ("module.b.aws_kms_key.this[0]", "create"),
            ("module.b.aws_s3_bucket.this", "create"),
        ]

    def test_plan_hash_ignores_metadata_but_not_changes(self):
        same = dict(PLAN, timestamp="2030-01-01T00:00:00Z")
        assert plan_hash(PLAN) == plan_hash(same)
        changed = {"resource_changes": PLAN["resource_changes"][:-1]}
        assert plan_hash(PLAN) != plan_hash(changed)

    def test_policy_result(self):
        result = policy_result(CONFTEST)
        assert result.evaluated and not result.passed
        assert result.failures == ("[iacp.tags.required] x: missing tag",)
        assert result.warnings == ("[iacp.s3.policy_unknown] y: unknown",)
        assert policy_result([{"failures": [], "warnings": None}]).passed
        assert not policy_result(None).evaluated and not policy_result(None).passed


class TestSummary:
    def test_counts_and_ok(self):
        summary = summarize("dev-x", "environments/dev/x", PLAN, [{"failures": []}], STEPS)
        assert summary.counts == {"create": 2, "update": 1, "replace": 1, "delete": 1}
        assert summary.ok
        assert summary.to_dict()["plan_hash"] == plan_hash(PLAN)

    @pytest.mark.parametrize(
        "steps,conftest",
        [
            ({"plan": "failure"}, [{"failures": []}]),
            (STEPS, CONFTEST),
            (STEPS, None),  # conftest never ran: not ok
        ],
    )
    def test_not_ok(self, steps, conftest):
        assert not summarize("id", "p", PLAN, conftest, steps).ok

    def test_markdown_has_marker_counts_and_findings(self):
        summary = summarize("development__platform__smoke", "environments/development/platform/smoke", PLAN, CONFTEST, STEPS)
        md = render_markdown(summary, plan_text="Plan: 2 to add", commit="0123456789abcdef", run_url="https://example/run/1")
        assert md.startswith("<!-- iacp-plan:development__platform__smoke -->\n")
        assert "(failed)" in md
        assert "2 to add, 1 to change, 1 to replace, 1 to destroy" in md
        assert "- deny: [iacp.tags.required] x: missing tag" in md
        assert "- warn: [iacp.s3.policy_unknown] y: unknown" in md
        assert "| policy (conftest) | failed |" in md
        assert "commit `0123456789ab`" in md and "[workflow run](https://example/run/1)" in md

    def test_markdown_without_plan(self):
        md = render_markdown(PlanSummary("id", "path", steps={"init": "failure"}))
        assert "not produced" in md and "| policy (conftest) | not run |" in md

    @pytest.mark.parametrize(
        "text,fence",
        [
            ("plain", "```text"),
            ("   ```", "````text"),  # indented closing fence
            ("```   ", "````text"),  # trailing spaces
            ("x ````` y", "``````text"),  # backticks mid-line
        ],
    )
    def test_fence_outlasts_backtick_runs(self, text, fence):
        md = render_markdown(PlanSummary("id", "path", policy=PolicyResult(evaluated=True)), plan_text=text)
        assert f"\n{fence}\n" in md and md.count(fence[:-4] + "\n") >= 1

    def test_plan_text_is_truncated_and_fenced_safely(self):
        text = "```\n" + "x" * 50_000
        md = render_markdown(PlanSummary("id", "path", policy=PolicyResult(evaluated=True)), plan_text=text)
        assert "truncated" in md
        assert "````text" in md  # fence longer than the backtick run inside the output
        assert len(md) < 65_536


class TestCli:
    def test_parse_steps(self):
        assert parse_steps(["fmt=success", "plan="]) == {"fmt": "success", "plan": "skipped"}
        with pytest.raises(ValueError):
            parse_steps(["nonsense"])

    def test_context_command(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke")
        assert main(["--repo-root", str(tmp_path), "context", str(root)]) == 0
        assert json.loads(capsys.readouterr().out)["iacp"]["context"]["environment"] == "development"

    def test_context_command_rejects_non_stack(self, tmp_path, capsys):
        assert main(["--repo-root", str(tmp_path), "context", str(tmp_path)]) == 1
        assert "not a stack root" in capsys.readouterr().err

    def test_summary_command(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke")
        (tmp_path / "plan.json").write_text(json.dumps(PLAN))
        (tmp_path / "conftest.json").write_text(json.dumps([{"failures": [], "warnings": []}]))
        (tmp_path / "plan.txt").write_text("Plan: 2 to add")
        md, js = tmp_path / "c.md", tmp_path / "s.json"
        argv = ["--repo-root", str(tmp_path), "summary", str(root), "--plan", str(tmp_path / "plan.json")]
        argv += ["--conftest", str(tmp_path / "conftest.json"), "--plan-text", str(tmp_path / "plan.txt")]
        argv += ["--step", "fmt=success", "--step", "plan=success", "--markdown-out", str(md), "--json-out", str(js)]
        assert main(argv) == 0
        assert json.loads(js.read_text())["ok"] is True
        assert "Plan: 2 to add" in md.read_text()

    def test_summary_command_with_missing_inputs(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke")
        (tmp_path / "conftest.json").write_text("not json")
        md, js = tmp_path / "c.md", tmp_path / "s.json"
        argv = ["--repo-root", str(tmp_path), "summary", str(root), "--plan", str(tmp_path / "missing.json")]
        argv += ["--conftest", str(tmp_path / "conftest.json"), "--step", "init=failure"]
        argv += ["--markdown-out", str(md), "--json-out", str(js)]
        assert main(argv) == 0
        data = json.loads(js.read_text())
        assert data["ok"] is False and data["plan_hash"] is None
