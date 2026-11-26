"""Drift: pure classification and payloads, the API adapter and the ``drift`` CLI."""

import json

import pytest

from tools.stack_pipeline.api_client import ApiError, Credential, IacpApiClient
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.drift import (
    MAX_CHANGED,
    DriftOutcome,
    Finding,
    build_outcome,
    classify_exit,
    counts,
    drift_hash,
    one_line,
    outcome_from_dict,
    parse_refresh_plan,
    summarize_error,
)
from tools.stack_pipeline.report import GitHubRun, ReportError

from .test_api_client import INTERNAL, SHA, STACK, api, client  # noqa: F401 (api is a fixture)

PATH = "environments/development/payments/orders"
GH = GitHubRun(run_id=42, run_attempt=1, head_sha=SHA, log_url="https://github.com/acme/iacp/actions/runs/42/attempts/1")


def drift_entry(address, actions, before=None, after=None, rtype="aws_sqs_queue"):
    return {"address": address, "type": rtype, "change": {"actions": actions, "before": before, "after": after}}


PLAN = {"format_version": "1.2", "resource_drift": [
    drift_entry("module.q.aws_sqs_queue.this", ["update"], {"visibility_timeout": 30, "kms": "a"}, {"visibility_timeout": 60, "kms": "a"}),
    drift_entry("module.b.aws_s3_bucket.this", ["delete"], {"bucket": "x"}, None, "aws_s3_bucket"),
    drift_entry("module.n.aws_sns_topic.this", ["no-op"]),
]}


class TestClassify:
    @pytest.mark.parametrize("code,drifted,want", [(0, 0, "clean"), (1, 0, "error"), (2, 3, "drifted"), (2, 0, "clean")])
    def test_classify_exit(self, code, drifted, want):
        assert classify_exit(code, drifted) == want

    @pytest.mark.parametrize("code", [-1, 3, 137])
    def test_unknown_exit_codes(self, code):
        with pytest.raises(ReportError):
            classify_exit(code, 0)


class TestParse:
    def test_findings_are_sorted_and_no_ops_dropped(self):
        found = parse_refresh_plan(PLAN)
        assert [f.address for f in found] == ["module.b.aws_s3_bucket.this", "module.q.aws_sqs_queue.this"]
        assert found[1].attributes == ("visibility_timeout",) and found[0].attributes == ()
        assert counts(found) == {"drifted": 2, "updated": 1, "deleted": 1}

    def test_no_drift_key_is_empty(self):
        assert parse_refresh_plan({"format_version": "1.2"}) == []

    @pytest.mark.parametrize("plan", [
        [], {"resource_drift": "x"}, {"resource_drift": [1]}, {"resource_drift": [{"address": "a"}]},
        {"resource_drift": [{"address": 1, "change": {"actions": ["update"]}}]},
        {"resource_drift": [{"address": "a", "change": {"actions": [1]}}]},
    ])
    def test_unreadable_plans_raise(self, plan):
        with pytest.raises(ReportError):
            parse_refresh_plan(plan)

    def test_a_replaced_resource_counts_once(self):
        f = [Finding("a.b", "t", ("delete", "create")), Finding("c.d", "t", ("update",))]
        assert counts(f) == {"drifted": 2, "updated": 1, "deleted": 1}

    def test_hash_covers_names_not_values(self):
        a = parse_refresh_plan(PLAN)
        changed = json.loads(json.dumps(PLAN))
        changed["resource_drift"][0]["change"]["after"]["visibility_timeout"] = 999
        assert drift_hash(parse_refresh_plan(changed)) == drift_hash(a)  # same attribute drifted
        changed["resource_drift"][0]["change"]["after"]["kms"] = "b"
        assert drift_hash(parse_refresh_plan(changed)) != drift_hash(a)  # another attribute drifted
        assert len(drift_hash(a)) == 64

    def test_values_never_reach_the_result(self):
        plan = json.loads(json.dumps(PLAN))
        plan["resource_drift"][0]["change"]["after"]["password"] = "hunter2-secret"
        out = build_outcome(PATH, 2, plan)
        assert "hunter2" not in json.dumps(out.to_dict()) and "hunter2" not in json.dumps(out.to_payload(GH))


class TestBuildOutcome:
    def test_clean(self):
        out = build_outcome(PATH, 0, {"format_version": "1.2"})
        assert (out.result, out.terraform_exit_code, out.plan_hash, out.changed) == ("clean", 0, "", ())
        assert out.problems(GH) == []

    def test_drifted(self):
        out = build_outcome(PATH, 2, PLAN)
        assert out.result == "drifted" and out.summary == {"drifted": 2, "updated": 1, "deleted": 1}
        assert len(out.plan_hash) == 64 and out.problems(GH) == []
        assert out.to_payload(GH) == {
            "stack_path": PATH, "result": "drifted", "terraform_exit_code": 2,
            "summary": {"drifted": 2, "updated": 1, "deleted": 1},
            "changed": [{"address": "module.b.aws_s3_bucket.this", "type": "aws_s3_bucket", "actions": ["delete"]},
                        {"address": "module.q.aws_sqs_queue.this", "type": "aws_sqs_queue", "actions": ["update"]}],
            "plan_hash": out.plan_hash,
            "github": {"run_id": 42, "run_attempt": 1, "head_sha": SHA},
            "log_url": GH.log_url,
        }

    def test_exit_two_with_only_output_changes_is_clean(self):
        out = build_outcome(PATH, 2, {"format_version": "1.2", "resource_drift": []})
        assert (out.result, out.terraform_exit_code) == ("clean", 2) and out.problems(GH) == []

    def test_a_failed_check_is_an_error_not_drift(self):
        out = build_outcome(PATH, 1, None, "Error: init failed\n  with module.x\n")
        assert (out.result, out.terraform_exit_code, out.error) == ("error", 1, "Error: init failed")
        assert out.to_payload(GH)["error"] == "Error: init failed" and "plan_hash" not in out.to_payload(GH)

    def test_only_a_redacted_summary_line_leaves_terraforms_output(self):
        text = "Error: AccessDenied password=hunter2 AKIAABCDEFGHIJKLMNOP\n  body: {\"secret\": \"s3cr3t\"}\n  more detail"
        out = build_outcome(PATH, 1, None, text)
        assert out.error == "Error: AccessDenied password= <redacted> <redacted>"
        assert "hunter2" not in json.dumps(out.to_payload(GH)) and "s3cr3t" not in json.dumps(out.to_payload(GH))

    @pytest.mark.parametrize("text,want", [
        ("", ""), ("\n\u2502 Error: boom\n\u2502 more", "Error: boom"),
        ("Error: Authorization failed with Bearer abc.def", "Error: Authorization failed with <redacted>"),
        ("Error: x" + "y" * 400, None),
    ])
    def test_summarize_error(self, text, want):
        got = summarize_error(text)
        assert "\n" not in got and len(got) <= 200
        if want is not None:
            assert got == want

    def test_a_failed_check_without_a_message_still_says_something(self):
        assert build_outcome(PATH, 1).error == "terraform plan -refresh-only failed; see the workflow log"

    @pytest.mark.parametrize("code,plan", [(0, None), (2, None), (3, PLAN), (137, None)])
    def test_unusable_results_become_errors_never_clean(self, code, plan):
        out = build_outcome(PATH, code, plan)
        assert out.result == "error" and out.error and out.problems(GH) == []

    def test_a_malformed_plan_is_an_error(self):
        out = build_outcome(PATH, 2, {"resource_drift": [1]})
        assert out.result == "error" and "resource_drift" in out.error

    def test_the_list_is_bounded_but_the_counts_are_not(self):
        plan = {"resource_drift": [drift_entry(f"aws_sqs_queue.q{i:04d}", ["update"]) for i in range(MAX_CHANGED + 50)]}
        out = build_outcome(PATH, 2, plan)
        assert len(out.changed) == MAX_CHANGED and out.summary["drifted"] == MAX_CHANGED + 50 and out.problems(GH) == []

    def test_one_line(self):
        assert "\n" not in one_line("a\n::error::b\r\nc") and len(one_line("x" * 5000)) <= 1000


class TestProblems:
    def good(self, **kw):
        return DriftOutcome(**{**build_outcome(PATH, 2, PLAN).__dict__, **kw})

    @pytest.mark.parametrize("patch,pointer", [
        ({"stack_path": "../etc"}, "/stack_path"),
        ({"result": "clean"}, "/result"),
        ({"terraform_exit_code": 3}, "/terraform_exit_code"),
        ({"plan_hash": ""}, "/plan_hash"),
        ({"plan_hash": "XYZ"}, "/plan_hash"),
        ({"summary": {"drifted": 2, "updated": 2, "deleted": 1}}, "/summary"),
        ({"error": "boom"}, "/error"),
        ({"summary": {"drifted": -1, "updated": 0, "deleted": 0}}, "/summary"),
        ({"changed": (Finding("a; rm -rf /", "t", ("update",)),)}, "/changed/0/address"),
        ({"changed": (Finding("a.b", "t", ()),)}, "/changed/0/actions"),
    ])
    def test_rejects(self, patch, pointer):
        assert any(p.startswith(pointer) for p in self.good(**patch).problems(GH)), self.good(**patch).problems(GH)

    def test_a_bad_run_is_rejected(self):
        bad = GitHubRun(run_id=0, run_attempt=0, head_sha="abc")
        assert len(self.good().problems(bad)) == 3
        with pytest.raises(ReportError, match="invalid"):
            self.good().to_payload(bad)

    def test_clean_with_resources_is_refused(self):
        out = DriftOutcome(PATH, "clean", 0, {"drifted": 1, "updated": 0, "deleted": 0})
        assert any(p.startswith("/summary") for p in out.problems(GH))


class TestResultFiles:
    def test_round_trip(self):
        out = build_outcome(PATH, 2, PLAN)
        back = outcome_from_dict(json.loads(json.dumps(out.to_dict())))
        assert back.to_dict() == out.to_dict()

    @pytest.mark.parametrize("mutate", [
        lambda d: d.update(extra=1),
        lambda d: d.update(result="nope"),
        lambda d: d.update(terraform_exit_code=True),
        lambda d: d.update(summary={"drifted": "2", "updated": 1, "deleted": 1}),
        lambda d: d.update(summary={"drifted": 2}),
        lambda d: d.update(changed="x"),
        lambda d: d.update(changed=[{"address": "a"}]),
        lambda d: d.update(result="clean"),
        lambda d: d.update(stack_path="elsewhere"),
    ])
    def test_a_result_file_is_strict(self, mutate):
        doc = build_outcome(PATH, 2, PLAN).to_dict()
        mutate(doc)
        with pytest.raises(ReportError):
            outcome_from_dict(doc)

    @pytest.mark.parametrize("doc", [[], "x", 1, None])
    def test_not_an_object(self, doc):
        with pytest.raises(ReportError):
            outcome_from_dict(doc)


# ---- the API adapter ---------------------------------------------------------------


class TestApiAdapter:
    def test_drift_stacks(self, api):  # noqa: F811
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": PATH, "team": "payments"}]})
        got = client(api).drift_stacks(10)
        assert [(s.path, s.stack_id) for s in got] == [(PATH, STACK)]
        assert api.calls[0]["query"] == {"limit": ["10"]}
        assert api.calls[0]["headers"]["Authorization"] == f"Bearer {INTERNAL}"

    @pytest.mark.parametrize("body", [{}, {"stacks": "x"}, {"stacks": [{"path": "../x"}]}, {"stacks": ["x"]}])
    def test_a_malformed_list_fails_closed(self, api, body):  # noqa: F811
        api.reply("GET", "/v1/internal/drift/stacks", 200, body)
        with pytest.raises(ApiError):
            client(api).drift_stacks()

    def test_post_drift_retries_5xx_then_succeeds(self, api):  # noqa: F811
        api.reply("POST", "/v1/internal/drift", 503, {"code": "unavailable"})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True, "report_id": "r"})
        resp = client(api).post_drift({"x": 1})
        assert resp.body["accepted"] and len(api.calls) == 2 and api.calls[1]["body"] == {"x": 1}

    def test_post_drift_does_not_retry_a_422(self, api):  # noqa: F811
        api.reply("POST", "/v1/internal/drift", 422, {"code": "validation_failed", "errors": [{"pointer": "/result", "message": "no"}]})
        with pytest.raises(ApiError, match="/result") as exc:
            client(api).post_drift({})
        assert exc.value.status == 422 and len(api.calls) == 1

    def test_a_redirect_is_never_followed(self, api):  # noqa: F811
        api.reply("POST", "/v1/internal/drift", 307, None, {"Location": "http://127.0.0.1:1/steal"})
        with pytest.raises(ApiError, match="redirect"):
            client(api).post_drift({})
        assert len(api.calls) == 1

    def test_drift_is_sent_with_a_fresh_oidc_token_each_time(self, api):  # noqa: F811
        tokens = iter(["t1", "t2", "t3"])
        c = IacpApiClient(api.url, lambda: Credential(next(tokens), "oidc"), sleep=lambda _: None)
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": []})
        api.reply("POST", "/v1/internal/drift", 503, {})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True})
        c.drift_stacks()
        c.post_drift({})
        assert [call["headers"]["Authorization"] for call in api.calls] == ["Bearer t1", "Bearer t2", "Bearer t3"]


# ---- the CLI -----------------------------------------------------------------------


@pytest.fixture
def stacks(make_stack):
    return {
        "orders": make_stack("development", "payments", "orders"),
        "queue": make_stack("development", "payments", "queue"),
        "gone": make_stack("staging", "payments", "retired", lifecycle="destroyed"),
    }


class TestList:
    def test_lists_active_stacks_only(self, stacks, tmp_path, capsys):
        out = tmp_path / "gh_out"
        assert main(["--repo-root", str(tmp_path), "drift", "list", "--github-output", str(out)]) == 0
        lines = dict(line.split("=", 1) for line in out.read_text().splitlines())
        matrix = json.loads(lines["matrix"])
        assert [m["path"] for m in matrix] == ["environments/development/payments/orders", "environments/development/payments/queue"]
        assert lines["count"] == "2" and matrix[0]["id"] == "development__payments__orders"

    def test_dispatch_subset(self, stacks, tmp_path):
        out = tmp_path / "gh_out"
        paths = json.dumps(["environments/development/payments/queue"])
        assert main(["--repo-root", str(tmp_path), "drift", "list", "--paths", paths, "--github-output", str(out)]) == 0
        assert "count=1" in out.read_text()

    @pytest.mark.parametrize("paths", ['["environments/staging/payments/retired"]', '["environments/development/payments/none"]',
                                       "not json", "[]", '["../x"]', '[1]', '{"a":1}'])
    def test_dispatch_input_is_validated(self, stacks, tmp_path, paths, capsys):
        assert main(["--repo-root", str(tmp_path), "drift", "list", "--paths", paths]) == 2
        err = capsys.readouterr().err
        assert err.startswith("error:") and "\n::" not in err

    def test_the_matrix_is_cut_with_a_warning(self, stacks, tmp_path, capsys):
        out = tmp_path / "gh_out"
        assert main(["--repo-root", str(tmp_path), "drift", "list", "--max-stacks", "1", "--github-output", str(out)]) == 0
        assert "count=1" in out.read_text() and "::warning" in capsys.readouterr().out


class TestScan:
    def run_scan(self, tmp_path, stack, code, plan=None, extra=()):
        out = tmp_path / "result.json"
        args = ["--repo-root", str(tmp_path), "drift", "scan", str(stack), "--exit-code", str(code), "--out", str(out), *extra]
        if plan is not None:
            f = tmp_path / "drift.json"
            f.write_text(json.dumps(plan))
            args += ["--plan", str(f)]
        return main(args), out

    def test_drifted(self, stacks, tmp_path, capsys):
        rc, out = self.run_scan(tmp_path, stacks["orders"], 2, PLAN)
        assert rc == 0 and json.loads(out.read_text())["result"] == "drifted"
        assert "drifted (2 resources: 1 updated, 1 deleted)" in capsys.readouterr().out

    def test_clean(self, stacks, tmp_path):
        rc, out = self.run_scan(tmp_path, stacks["orders"], 0, {"format_version": "1.2"})
        assert rc == 0 and json.loads(out.read_text())["result"] == "clean"

    def test_a_failed_plan_is_a_warning_not_a_failed_job(self, stacks, tmp_path, capsys):
        rc, out = self.run_scan(tmp_path, stacks["orders"], 1, extra=["--error-message", "init failed\n::error::x"])
        printed = capsys.readouterr().out
        assert rc == 0 and json.loads(out.read_text())["result"] == "error"
        assert "::warning title=Drift check failed::" in printed and "\n::error::x" not in printed

    def test_a_missing_plan_with_exit_two_is_an_error_result(self, stacks, tmp_path):
        rc, out = self.run_scan(tmp_path, stacks["orders"], 2)
        assert rc == 0 and json.loads(out.read_text())["result"] == "error"

    def test_the_stack_comes_from_the_directory(self, stacks, tmp_path):
        rc, out = self.run_scan(tmp_path, stacks["queue"], 0, {})
        assert json.loads(out.read_text())["stack_path"] == "environments/development/payments/queue"


@pytest.fixture
def results(tmp_path):
    d = tmp_path / "results"
    d.mkdir()

    def write(name, outcome):
        (d / f"{name}.json").write_text(json.dumps(outcome.to_dict()))

    write("orders", build_outcome("environments/development/payments/orders", 2, PLAN))
    write("queue", build_outcome("environments/development/payments/queue", 0, {}))
    return d


@pytest.fixture
def runner_env(monkeypatch):
    for k in ("ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "IACP_API_URL", "IACP_OIDC_AUDIENCE",
              "IACP_INTERNAL_TOKEN"):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setenv("GITHUB_RUN_ID", "42")
    monkeypatch.setenv("GITHUB_RUN_ATTEMPT", "1")
    monkeypatch.setenv("GITHUB_SHA", SHA)
    monkeypatch.setenv("GITHUB_SERVER_URL", "https://github.com")
    monkeypatch.setenv("GITHUB_REPOSITORY", "acme/iacp")


QUEUE = "environments/development/payments/queue"


class TestReport:
    def args(self, api, results, *extra):
        return ["drift", "report", "--results-dir", str(results), "--api-url", api.url, "--skip-if-unconfigured", *extra]

    def test_skipped_without_an_api(self, results, runner_env, capsys):
        assert main(["drift", "report", "--results-dir", str(results), "--skip-if-unconfigured"]) == 0
        assert "::notice title=Platform API not configured" in capsys.readouterr().out

    def test_unconfigured_without_the_flag_is_an_error(self, results, runner_env):
        assert main(["drift", "report", "--results-dir", str(results)]) == 2

    def test_reports_every_due_stack(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": PATH}, {"stack_id": STACK, "path": QUEUE}]})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True, "report_id": "r1", "result": "drifted", "status": "open", "replayed": False})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True, "report_id": "r2", "result": "clean", "status": None, "replayed": True})
        assert main(self.args(api, results)) == 0
        posts = [c for c in api.calls if c["method"] == "POST"]
        assert [p["body"]["stack_path"] for p in posts] == [PATH, QUEUE]
        assert posts[0]["body"]["github"] == {"run_id": 42, "run_attempt": 1, "head_sha": SHA}
        assert posts[0]["body"]["summary"] == {"drifted": 2, "updated": 1, "deleted": 1}
        out = capsys.readouterr().out
        assert "reported environments/development/payments/orders: drifted -> report r1" in out and "(replay)" in out

    def test_a_stack_the_api_does_not_list_is_skipped(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": QUEUE}]})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True, "report_id": "r2", "result": "clean"})
        assert main(self.args(api, results)) == 0
        assert [c["body"]["stack_path"] for c in api.calls if c["method"] == "POST"] == [QUEUE]
        assert "is not due according to the API" in capsys.readouterr().out

    def test_a_due_stack_without_a_result_is_a_warning(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        other = "environments/development/payments/other"
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": other}]})
        assert main(self.args(api, results)) == 0
        assert f"::warning title=No drift result::{other}" in capsys.readouterr().out

    def test_a_stack_that_became_busy_is_a_notice(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": PATH}]})
        api.reply("POST", "/v1/internal/drift", 409, {"code": "stack_not_checkable", "detail": "the stack is updating"})
        assert main(self.args(api, results)) == 0
        assert "::notice title=Drift result skipped" in capsys.readouterr().out

    def test_a_rejected_report_fails_the_job_after_the_others_were_sent(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        api.reply("GET", "/v1/internal/drift/stacks", 200, {"stacks": [{"stack_id": STACK, "path": PATH}, {"stack_id": STACK, "path": QUEUE}]})
        api.reply("POST", "/v1/internal/drift", 403, {"code": "workflow_not_allowed", "detail": "no"})
        api.reply("POST", "/v1/internal/drift", 200, {"accepted": True, "report_id": "r2", "result": "clean"})
        assert main(self.args(api, results)) == 1
        assert len([c for c in api.calls if c["method"] == "POST"]) == 2
        assert "::error title=Drift report rejected" in capsys.readouterr().out

    def test_a_forged_result_file_sends_nothing(self, api, results, runner_env, monkeypatch, capsys):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        (results / "evil.json").write_text(json.dumps({"stack_path": PATH, "result": "clean", "terraform_exit_code": 0,
                                                      "summary": {"drifted": 0, "updated": 0, "deleted": 0}, "changed": [],
                                                      "severity": "high"}))
        assert main(self.args(api, results)) == 1
        assert api.calls == [] and "unknown fields" in capsys.readouterr().err

    def test_two_results_for_one_stack_are_refused(self, api, results, runner_env, monkeypatch):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        (results / "dup.json").write_text((results / "orders.json").read_text())
        assert main(self.args(api, results)) == 1 and api.calls == []

    def test_a_non_json_file_fails(self, api, results, runner_env):  # noqa: F811
        (results / "junk.json").write_text("not json")
        assert main(self.args(api, results)) == 1 and api.calls == []

    def test_the_api_list_failing_fails_closed(self, api, results, runner_env, monkeypatch):  # noqa: F811
        monkeypatch.setenv("IACP_INTERNAL_TOKEN", INTERNAL)
        api.reply("GET", "/v1/internal/drift/stacks", 403, {"code": "workflow_not_allowed"})
        assert main(self.args(api, results)) == 1
        assert not [c for c in api.calls if c["method"] == "POST"]

    def test_dry_run_prints_the_payloads(self, api, results, runner_env, capsys):  # noqa: F811
        assert main(["drift", "report", "--results-dir", str(results), "--dry-run"]) == 0
        printed = json.loads(capsys.readouterr().out)
        assert [p["stack_path"] for p in printed] == [PATH, QUEUE] and api.calls == []
