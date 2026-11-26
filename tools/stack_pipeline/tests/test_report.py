import pytest

from tools.stack_pipeline.report import (
    SENSITIVE,
    GitHubRun,
    ReportError,
    RunReport,
    api_base_url,
    describe_problem,
    error_block,
    needs_lock,
    plan_block,
    policy_block,
    redact_outputs,
    stage_status,
    summary_status,
)

SHA = "e4" * 20
RUN = GitHubRun(run_id=11873399021, run_attempt=1, head_sha=SHA)

SUMMARY = {
    "stack_id": "development__payments__order-events",
    "stack_path": "environments/development/payments/order-events",
    "ok": True,
    "plan_hash": "3f9a" * 16,
    "counts": {"create": 2, "update": 1, "replace": 0, "delete": 0},
    "steps": {"fmt": "success"},
    "policy": {"failures": [], "warnings": ["iacp.lambda.reserved_concurrency_recommended"]},
    "changes": [
        {"address": "module.q.aws_sqs_queue.this", "action": "create"},
        {"address": "module.q.aws_sqs_queue.dlq", "action": "create"},
        {"address": "aws_sqs_queue.x", "action": "update"},
    ],
}


def report(**overrides):
    fields = dict(
        pipeline="validate",
        stack_path="environments/development/payments/order-events",
        stage="summary",
        status="passed",
        github=RUN,
        request_short_id="req-7k3p9x",
        fencing_token=48213,
    )
    fields.update(overrides)
    return RunReport(**fields)


class TestRunReport:
    def test_payload_matches_the_backend_shape(self):
        payload = report(plan=plan_block(SUMMARY), policy=policy_block(SUMMARY)).to_payload()
        assert payload == {
            "pipeline": "validate",
            "stack_path": "environments/development/payments/order-events",
            "request_short_id": "req-7k3p9x",
            "fencing_token": 48213,
            "github": {"run_id": 11873399021, "run_attempt": 1, "head_sha": SHA},
            "stage": "summary",
            "status": "passed",
            "plan": plan_block(SUMMARY),
            "policy": policy_block(SUMMARY),
        }

    def test_only_fields_runs_report_declares(self):
        # backend decodes strictly: any other top-level key is a 400.
        declared = {"pipeline", "stack_path", "request_short_id", "fencing_token", "github", "stage", "status",
                    "log_url", "plan", "policy", "cost", "outputs", "terraform_exit_code", "error"}
        full = report(
            github=GitHubRun(1, 2, SHA, job_id=7, log_url="https://github.com/o/r/actions/runs/1/attempts/2"),
            plan={}, policy={}, outputs={"a": 1}, terraform_exit_code=0, error={"class": "permanent", "message": "x"},
        ).to_payload()
        assert set(full) <= declared
        assert full["github"] == {"run_id": 1, "run_attempt": 2, "head_sha": SHA, "job_id": 7}
        assert full["log_url"].endswith("/attempts/2")
        assert full["terraform_exit_code"] == 0  # zero is sent, not dropped

    def test_drift_needs_no_lock_or_request(self):
        payload = report(pipeline="drift", fencing_token=None, request_short_id="").to_payload()
        assert "fencing_token" not in payload and "request_short_id" not in payload

    @pytest.mark.parametrize(
        ("overrides", "pointers"),
        [
            ({"pipeline": "module_release"}, ["/pipeline"]),
            ({"stack_path": "../etc"}, ["/stack_path"]),
            ({"request_short_id": "nope"}, ["/request_short_id"]),
            ({"github": GitHubRun(0, 0, SHA)}, ["/github/run_id", "/github/run_attempt"]),
            ({"github": GitHubRun(1, 1, "ABC")}, ["/github/head_sha"]),
            ({"stage": "", "status": "ok"}, ["/stage", "/status"]),
            ({"pipeline": "apply", "fencing_token": None, "request_short_id": ""}, ["/fencing_token", "/request_short_id"]),
            ({"fencing_token": 0}, ["/fencing_token"]),
        ],
    )
    def test_problems_mirror_runs_validate(self, overrides, pointers):
        problems = report(**overrides).problems()
        assert [p.split(":", 1)[0] for p in problems] == pointers
        with pytest.raises(ReportError):
            report(**overrides).to_payload()

    def test_needs_lock(self):
        assert needs_lock("validate") and needs_lock("apply") and needs_lock("ttl_destroy")
        assert not needs_lock("drift")


class TestGitHubRun:
    ENV = {
        "GITHUB_RUN_ID": "42",
        "GITHUB_RUN_ATTEMPT": "3",
        "GITHUB_SHA": SHA,
        "GITHUB_SERVER_URL": "https://github.com",
        "GITHUB_REPOSITORY": "acme/repo",
    }

    def test_from_env_uses_github_sha(self):
        run = GitHubRun.from_env(self.ENV)
        assert (run.run_id, run.run_attempt, run.head_sha) == (42, 3, SHA)
        assert run.log_url == "https://github.com/acme/repo/actions/runs/42/attempts/3"

    def test_outside_actions(self):
        with pytest.raises(ReportError, match="GITHUB_RUN_ID"):
            GitHubRun.from_env({})


@pytest.mark.parametrize(
    ("value", "want"),
    [("passed", "passed"), ("success", "passed"), ("failure", "failed"), ("cancelled", "cancelled"),
     ("skipped", "cancelled"), (" In_Progress ", "in_progress")],
)
def test_stage_status(value, want):
    assert stage_status(value) == want


def test_stage_status_rejects_unknown():
    with pytest.raises(ReportError):
        stage_status("neutral")


def test_plan_block_uses_docs_counts():
    block = plan_block(SUMMARY)
    assert block["hash"] == SUMMARY["plan_hash"]
    assert block["counts"] == {"add": 2, "change": 1, "destroy": 0, "replace": 0}
    assert block["resource_changes"][0] == {"address": "module.q.aws_sqs_queue.this", "actions": ["create"]}


def test_plan_block_without_plan():
    assert plan_block({**SUMMARY, "plan_hash": None}) is None


def test_policy_block():
    assert policy_block(SUMMARY) == {"opa": {"deny": [], "warn": ["iacp.lambda.reserved_concurrency_recommended"]}}
    assert policy_block({}) is None


def test_summary_status():
    assert summary_status(SUMMARY) == "passed"
    assert summary_status({**SUMMARY, "ok": False}) == "failed"
    assert summary_status(None) == "failed"


def test_redact_outputs():
    outputs = {
        "bucket": {"sensitive": False, "type": "string", "value": "b-1"},
        "secret": {"sensitive": True, "type": "string", "value": "hunter2"},
        "odd": "not an output entry",
    }
    assert redact_outputs(outputs) == {"bucket": "b-1", "odd": SENSITIVE, "secret": SENSITIVE}


def test_error_block():
    assert error_block("  ") is None
    assert error_block("boom") == {"class": "permanent", "message": "boom"}
    assert error_block("x" * 5000, "transient")["message"].endswith("(truncated)")
    with pytest.raises(ReportError):
        error_block("boom", "fatal")


@pytest.mark.parametrize("url", ["https://api.example.com/", "http://localhost:8080", "http://127.0.0.1:8080/"])
def test_api_base_url_ok(url):
    assert not api_base_url(url).endswith("/")


@pytest.mark.parametrize("url", ["http://api.example.com", "ftp://x", "", "https://x/?a=1", "localhost:8080"])
def test_api_base_url_rejects(url):
    with pytest.raises(ReportError):
        api_base_url(url)


def test_describe_problem():
    body = {"code": "validation_failed", "title": "Unprocessable", "errors": [{"pointer": "/stage", "message": "bad"}]}
    assert describe_problem(body) == "validation_failed; Unprocessable; /stage bad"
    assert describe_problem({}, "plain") == "plain"
