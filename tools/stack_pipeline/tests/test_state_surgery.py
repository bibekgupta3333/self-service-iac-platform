"""Break-glass state surgery: input validation, commands, reports and the CLI."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tools.stack_pipeline import cli
from tools.stack_pipeline.state_surgery import (
    OP_FORCE_UNLOCK,
    OP_IMPORT,
    OP_STATE_MV,
    OP_STATE_RM,
    PLAN_ARGV,
    SurgeryInputError,
    SurgeryRequest,
    SurgeryResult,
    SurgeryRunner,
    TerraformRunner,
    build_report,
    error_excerpt,
    parse_request,
    refused_report,
    terraform_argv,
    validate_args,
)

STACK = "environments/development/payments/orders"
RUN = {"run_id": 77, "run_attempt": 1, "head_sha": "e4" * 20, "job_id": 9}


def inputs(**overrides: str) -> dict[str, str]:
    base = {"stack_path": STACK, "operation": OP_STATE_MV, "args": "aws_sqs_queue.a module.q.aws_sqs_queue.this",
            "ticket": "INC-1234", "requested_by": "priya-s", "dry_run": "true"}
    base.update(overrides)
    return base


@pytest.mark.parametrize(
    ("op", "args", "ok"),
    [
        (OP_STATE_RM, ["module.queue.aws_sqs_queue.this"], True),
        (OP_STATE_RM, ["aws_s3_bucket.logs", 'module.buckets["eu-west-1"].aws_s3_bucket.this[0]'], True),
        (OP_STATE_RM, ["module.vpc.data.aws_availability_zones.available"], True),
        (OP_STATE_RM, [], False),
        (OP_STATE_RM, ["aws_sqs_queue.q"] * 11, False),
        (OP_STATE_MV, ["module.queue.aws_sqs_queue.this", "module.orders.aws_sqs_queue.this"], True),
        (OP_STATE_MV, ["aws_sqs_queue.a", "aws_sqs_queue.a"], False),
        (OP_STATE_MV, ["aws_sqs_queue.a"], False),
        (OP_IMPORT, ["module.queue.aws_sqs_queue.this", "https://sqs.us-east-1.amazonaws.com/000000000000/orders"], True),
        (OP_IMPORT, ["aws_iam_role.r", "arn:aws:iam::000000000000:role/iacp-orders"], True),
        (OP_FORCE_UNLOCK, ["9db590f1-b6fe-c5f2-2678-8804f089deba"], True),
        (OP_FORCE_UNLOCK, ["all"], False),
        ("state push", ["terraform.tfstate"], False),
        ("apply", [], False),
        # Injection attempts: none is an address, an id or a lock id.
        (OP_STATE_RM, ["aws_sqs_queue.a;rm"], False),
        (OP_STATE_RM, ["-state=/etc/passwd"], False),
        (OP_STATE_MV, ["aws_sqs_queue.a", "-state-out=x"], False),
        (OP_IMPORT, ["aws_s3_bucket.b", "$(curl evil)"], False),
        (OP_IMPORT, ["aws_s3_bucket.b", "`id`"], False),
        (OP_STATE_RM, ['module.m["a\\"b"].aws_s3_bucket.this'], False),
        (OP_FORCE_UNLOCK, ["-force"], False),
        (OP_STATE_RM, ["aws_sqs_queue." + "a" * 300], False),
    ],
)
def test_validate_args(op: str, args: list[str], ok: bool) -> None:
    assert (validate_args(op, args) == []) is ok


def test_parse_request_accepts_valid_inputs() -> None:
    req = parse_request(inputs(stack_path=STACK + "/"))
    assert req == SurgeryRequest(stack_path=STACK, operation=OP_STATE_MV,
                                 args=("aws_sqs_queue.a", "module.q.aws_sqs_queue.this"), ticket="INC-1234",
                                 requested_by="priya-s", dry_run=True)
    assert SurgeryRequest.from_dict(req.to_dict()) == req


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("stack_path", "environments/../../etc"),
        ("stack_path", "environments/development/payments/orders\n::set-env name=X::y"),
        ("operation", "state push"),
        ("operation", "state rm; curl evil"),
        ("args", "aws_sqs_queue.a\n::add-mask::x"),
        ("args", "x" * 5000),
        ("ticket", "see slack"),
        ("ticket", "INC-1234\n::warning::x"),
        ("requested_by", "priya s"),
        ("dry_run", "maybe"),
    ],
)
def test_parse_request_refuses_without_echoing(field: str, value: str) -> None:
    with pytest.raises(SurgeryInputError) as exc:
        parse_request(inputs(**{field: value}))
    message = str(exc.value)
    # The message names the input but never repeats what was typed.
    assert "::" not in message and "curl" not in message and "slack" not in message and "\n" not in message


def test_terraform_argv() -> None:
    mv = parse_request(inputs())
    assert terraform_argv(mv) == ["terraform", "state", "mv", "-dry-run", "aws_sqs_queue.a", "module.q.aws_sqs_queue.this"]
    rm = parse_request(inputs(operation=OP_STATE_RM, args="aws_sqs_queue.a", dry_run="false"))
    assert terraform_argv(rm) == ["terraform", "state", "rm", "aws_sqs_queue.a"]
    imp = parse_request(inputs(operation=OP_IMPORT, args="aws_s3_bucket.b iacp-orders", dry_run="false"))
    assert terraform_argv(imp) == ["terraform", "import", "-input=false", "-no-color", "aws_s3_bucket.b", "iacp-orders"]
    unlock = parse_request(inputs(operation=OP_FORCE_UNLOCK, args="9db590f1-b6fe-c5f2", dry_run="false"))
    assert terraform_argv(unlock) == ["terraform", "force-unlock", "-force", "9db590f1-b6fe-c5f2"]
    # A dry run of an operation without -dry-run runs nothing.
    assert terraform_argv(parse_request(inputs(operation=OP_IMPORT, args="aws_s3_bucket.b x1"))) is None
    assert "-lock=false" in PLAN_ARGV and "-detailed-exitcode" in PLAN_ARGV


class FakeRunner(TerraformRunner):
    def __init__(self, stack_dir: Path, codes: list[int], mutate: bool = False) -> None:
        super().__init__(stack_dir)
        self.codes, self.mutate = codes, mutate

    def run(self, argv):  # type: ignore[override]
        self.calls.append(list(argv))
        if self.mutate and argv[:2] == ["terraform", "state"]:
            (self.stack_dir / "terraform.tfstate").write_text('{"serial": 2}')
        return self.codes.pop(0)


def test_surgery_runner(tmp_path: Path) -> None:
    (tmp_path / "terraform.tfstate").write_text('{"serial": 1}')
    runner = FakeRunner(tmp_path, [0, 2], mutate=True)
    result = SurgeryRunner(runner).run(parse_request(inputs(dry_run="false")))
    assert result.outcome == "succeeded" and result.exit_code == 0 and result.plan_exit_code == 2
    assert result.state_before_sha256 and result.state_after_sha256 and result.state_before_sha256 != result.state_after_sha256
    assert runner.calls[1] == PLAN_ARGV

    failed = SurgeryRunner(FakeRunner(tmp_path, [1])).run(parse_request(inputs(dry_run="false")))
    assert failed.outcome == "failed" and failed.plan_exit_code is None  # no plan after a failed command

    nothing = FakeRunner(tmp_path, [0])
    dry = SurgeryRunner(nothing).run(parse_request(inputs(operation=OP_IMPORT, args="aws_s3_bucket.b x1")))
    assert dry.outcome == "succeeded" and dry.exit_code is None and nothing.calls == [PLAN_ARGV]


def test_reports() -> None:
    req = parse_request(inputs())
    body = build_report(req, SurgeryResult(outcome="succeeded", exit_code=0, plan_exit_code=0, state_before_sha256="a" * 64), RUN)
    assert body["operation"] == OP_STATE_MV and body["args"] == list(req.args) and body["exit_code"] == 0
    assert body["github"] == {"run_id": 77, "run_attempt": 1, "head_sha": "e4" * 20}
    assert "state_after_sha256" not in body

    refused = refused_report(inputs(operation="state push", args="; rm -rf /", ticket="nope"), RUN)
    assert refused["outcome"] == "refused" and refused["args"] == [] and refused["operation"] == "" and refused["ticket"] == ""
    assert refused["stack_path"] == STACK and refused["requested_by"] == "priya-s"
    assert refused_report(inputs(requested_by="a b"), RUN)["requested_by"] == "unknown"


def test_result_from_untrusted_file() -> None:
    r = SurgeryResult.from_dict({"outcome": "pwned", "exit_code": True, "state_before_sha256": "zz"})
    assert r == SurgeryResult(outcome="failed")


def test_error_excerpt() -> None:
    log = "\x1b[31mmodule.q.aws_sqs_queue.this: Creating...\x1b[0m\n" \
          "│ Error: creating SQS Queue: api error ThrottlingException: Rate exceeded\n│\n" \
          "::set-output name=x::y\n  with module.q.aws_sqs_queue.this"
    out = error_excerpt(log)
    assert out.startswith("│ Error: creating SQS Queue") and "ThrottlingException" in out
    assert "\x1b" not in out and "\n::" not in out
    assert error_excerpt("a\nb") == "a\nb"
    assert len(error_excerpt("Error: " + "x" * 5000)) == 2000


# ---- CLI --------------------------------------------------------------------------------


def surgery_env(monkeypatch: pytest.MonkeyPatch, **values: str) -> None:
    env = {"IN_STACK_PATH": STACK, "IN_OPERATION": OP_STATE_RM, "IN_ARGS": "aws_sqs_queue.a", "IN_TICKET": "INC-1234",
           "IN_DRY_RUN": "true", "GITHUB_ACTOR": "priya-s", "GITHUB_RUN_ID": "77", "GITHUB_RUN_ATTEMPT": "1",
           "GITHUB_SHA": "e4" * 20, "IACP_API_URL": ""}
    env.update(values)
    for k, v in env.items():
        monkeypatch.setenv(k, v)


def test_cli_validate_writes_the_request(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    surgery_env(monkeypatch)
    out = tmp_path / "request.json"
    assert cli.main(["state-surgery", "validate", "--request", str(out)]) == 0
    assert json.loads(out.read_text())["args"] == ["aws_sqs_queue.a"]
    assert "validated: terraform state rm on " + STACK in capsys.readouterr().out


def test_cli_validate_refuses_without_echoing(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                              capsys: pytest.CaptureFixture[str]) -> None:
    surgery_env(monkeypatch, IN_ARGS="aws_sqs_queue.a\n::stop-commands::pwned", IN_OPERATION="state push")
    out = tmp_path / "request.json"
    assert cli.main(["state-surgery", "validate", "--request", str(out)]) == 1
    printed = capsys.readouterr().out
    assert "pwned" not in printed and "state push" not in printed and "::error title=state-surgery input refused::" in printed
    assert not out.exists()


def test_cli_run_and_report_local_first(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                        capsys: pytest.CaptureFixture[str]) -> None:
    surgery_env(monkeypatch)
    stack = tmp_path / STACK
    stack.mkdir(parents=True)
    (stack / "iacp.stack.json").write_text("{}")
    request, result = tmp_path / "request.json", tmp_path / "result.json"
    assert cli.main(["state-surgery", "validate", "--request", str(request)]) == 0

    calls: list[list[str]] = []

    def fake_run(self, argv):  # noqa: ANN001
        calls.append(list(argv))
        return 0

    monkeypatch.setattr(TerraformRunner, "run", fake_run)
    assert cli.main(["--repo-root", str(tmp_path), "state-surgery", "run", "--request", str(request), "--result", str(result)]) == 0
    printed = capsys.readouterr().out
    token = printed.split("::stop-commands::", 1)[1].split("\n", 1)[0]
    assert f"::{token}::" in printed  # command processing resumed after Terraform ran
    assert calls[0] == ["terraform", "state", "rm", "-dry-run", "aws_sqs_queue.a"] and calls[1] == PLAN_ARGV

    # No API (local-first): the report is printed with a notice instead of sent.
    assert cli.main(["state-surgery", "report", "--request", str(request), "--result", str(result),
                     "--skip-if-unconfigured"]) == 0
    printed = capsys.readouterr().out
    body = json.loads(printed[printed.index("{"):])
    assert body["outcome"] == "succeeded" and body["exit_code"] == 0 and body["github"]["run_id"] == 77


def test_cli_report_refused_when_validation_failed(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                                   capsys: pytest.CaptureFixture[str]) -> None:
    surgery_env(monkeypatch, IN_ARGS="; rm -rf /")
    assert cli.main(["state-surgery", "report", "--request", str(tmp_path / "missing.json"), "--skip-if-unconfigured"]) == 0
    printed = capsys.readouterr().out
    body = json.loads(printed[printed.index("{"):])
    assert body["outcome"] == "refused" and body["args"] == [] and "rm -rf" not in printed
