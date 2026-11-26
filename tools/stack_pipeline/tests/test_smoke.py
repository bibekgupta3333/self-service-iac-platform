import json
import subprocess

import pytest

from tools.stack_pipeline.adapters import AwsCliTagReader
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.smoke import (
    PROBES,
    AppliedResource,
    SmokeConfigError,
    SmokeProbeError,
    SmokeTester,
    applied_resources,
    declared_outputs,
    local_endpoint,
    output_checks,
    required_tags,
    state_tag_checks,
    tag_problems,
)

from .conftest import metadata

LOCAL = "http://localhost:4566"

TAGS = {
    "iacp:team": "platform",
    "iacp:environment": "development",
    "iacp:stack": "smoke",
    "iacp:managed-by": "iacp",
}

# Trimmed from a real apply of environments/development/platform/smoke on LocalStack.
PLAN = {"configuration": {"root_module": {"outputs": {"smoke_bucket": {"description": "Outputs of smoke_bucket"}}}}}
OUTPUTS = {"smoke_bucket": {"sensitive": False, "value": {"id": "platform-development-smoke"}}}
STATE = {
    "values": {
        "root_module": {
            "child_modules": [
                {
                    "address": "module.smoke_bucket",
                    "resources": [
                        {"address": "module.smoke_bucket.data.aws_partition.current", "mode": "data",
                         "type": "aws_partition", "values": {}},
                        {"address": "module.smoke_bucket.aws_s3_bucket.this", "mode": "managed", "type": "aws_s3_bucket",
                         "values": {"bucket": "platform-development-smoke", "tags_all": dict(TAGS, **{"iacp:module": "s3"})}},
                        {"address": "module.smoke_bucket.aws_kms_key.this[0]", "mode": "managed", "type": "aws_kms_key",
                         "values": {"key_id": "k-1", "tags_all": TAGS}},
                        {"address": "module.smoke_bucket.aws_kms_alias.this[0]", "mode": "managed",
                         "type": "aws_kms_alias", "values": {"name": "alias/x"}},
                    ],
                    "child_modules": [
                        {"resources": [{"address": "module.smoke_bucket.module.n.aws_sqs_queue.q", "mode": "managed",
                                        "type": "aws_sqs_queue", "values": {"url": "http://q", "tags_all": TAGS}}]}
                    ],
                }
            ]
        }
    }
}


class FakeReader:
    """In-memory TagReader: responses keyed by (service, operation, identifier)."""

    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def read(self, argv):
        self.calls.append(list(argv))
        key = (argv[0], argv[1], argv[3])
        if key not in self.responses:
            raise SmokeProbeError(f"{argv[1]}: not found")
        return self.responses[key]


LIVE_OK = {
    ("s3api", "get-bucket-tagging", "platform-development-smoke"): {
        "TagSet": [{"Key": k, "Value": v} for k, v in TAGS.items()]
    },
    ("kms", "list-resource-tags", "k-1"): {"Tags": [{"TagKey": k, "TagValue": v} for k, v in TAGS.items()]},
    ("sqs", "list-queue-tags", "http://q"): {"Tags": dict(TAGS)},
}


class TestExpectations:
    def test_required_tags(self):
        assert required_tags(metadata("development", "platform", "smoke")) == TAGS

    def test_tag_problems(self):
        assert tag_problems(TAGS, TAGS) == ()
        assert tag_problems({"iacp:team": "x", **{k: v for k, v in TAGS.items() if k != "iacp:team"}}, TAGS) == (
            "iacp:team='x', expected 'platform'",
        )
        assert tag_problems({}, {"iacp:stack": "s"}) == ("iacp:stack is missing",)

    def test_applied_resources_walks_child_modules_and_skips_data(self):
        addresses = [r.address for r in applied_resources(STATE)]
        assert addresses == [
            "module.smoke_bucket.aws_kms_alias.this[0]",
            "module.smoke_bucket.aws_kms_key.this[0]",
            "module.smoke_bucket.aws_s3_bucket.this",
            "module.smoke_bucket.module.n.aws_sqs_queue.q",
        ]
        assert applied_resources({}) == ()

    def test_declared_outputs(self):
        assert declared_outputs(PLAN) == ("smoke_bucket",)
        assert declared_outputs({}) == ()

    @pytest.mark.parametrize(
        "outputs,detail",
        [
            ({}, "missing from terraform output"),
            ({"smoke_bucket": {"sensitive": False}}, "missing from terraform output"),
            ({"smoke_bucket": {"sensitive": False, "value": None}}, "value is null"),
        ],
    )
    def test_output_checks_fail(self, outputs, detail):
        (check,) = output_checks(("smoke_bucket",), outputs)
        assert not check.passed and check.detail == detail

    def test_sensitive_output_passes_without_revealing_it(self):
        (check,) = output_checks(("pw",), {"pw": {"sensitive": True, "value": "s3cr3t"}})
        assert check.passed and "s3cr3t" not in check.detail

    def test_state_tag_checks_only_tagged_resources(self):
        resources = (
            AppliedResource("a", "aws_s3_bucket", {"tags_all": TAGS}),
            AppliedResource("b", "aws_kms_alias", {}),
            AppliedResource("c", "aws_sqs_queue", {"tags_all": {"iacp:team": "platform"}}),
            AppliedResource("d", "aws_sqs_queue", {"tags_all": None}),
        )
        checks = state_tag_checks(resources, TAGS)
        assert [(c.subject, c.passed) for c in checks] == [("a", True), ("c", False), ("d", False)]


class TestProbes:
    @pytest.mark.parametrize(
        "rtype,values,argv",
        [
            ("aws_s3_bucket", {"bucket": "b", "id": "x"}, ["s3api", "get-bucket-tagging", "--bucket", "b"]),
            ("aws_s3_bucket", {"bucket": "", "id": "x"}, ["s3api", "get-bucket-tagging", "--bucket", "x"]),
            ("aws_kms_key", {"key_id": "k"}, ["kms", "list-resource-tags", "--key-id", "k"]),
            ("aws_sqs_queue", {"url": "http://q"}, ["sqs", "list-queue-tags", "--queue-url", "http://q"]),
            ("aws_lambda_function", {"arn": "arn:l"}, ["lambda", "list-tags", "--resource", "arn:l"]),
            ("aws_iam_role", {"name": "r"}, ["iam", "list-role-tags", "--role-name", "r"]),
            ("aws_cloudwatch_log_group", {"name": "/l"}, ["logs", "list-tags-log-group", "--log-group-name", "/l"]),
            ("aws_lambda_function", {}, None),
        ],
    )
    def test_argv(self, rtype, values, argv):
        assert PROBES[rtype].argv(values) == argv

    @pytest.mark.parametrize(
        "rtype,response",
        [
            ("aws_s3_bucket", {"TagSet": [{"Key": "iacp:team", "Value": "platform"}]}),
            ("aws_kms_key", {"Tags": [{"TagKey": "iacp:team", "TagValue": "platform"}]}),
            ("aws_sqs_queue", {"Tags": {"iacp:team": "platform"}}),
            ("aws_lambda_function", {"Tags": {"iacp:team": "platform"}}),
            ("aws_iam_role", {"Tags": [{"Key": "iacp:team", "Value": "platform"}]}),
            ("aws_cloudwatch_log_group", {"tags": {"iacp:team": "platform"}}),
            ("aws_cloudwatch_metric_alarm", {"Tags": [{"Key": "iacp:team", "Value": "platform"}]}),
            ("aws_sns_topic", {"Tags": [{"Key": "iacp:team", "Value": "platform"}]}),
            ("aws_secretsmanager_secret", {"Tags": [{"Key": "iacp:team", "Value": "platform"}]}),
            ("aws_cloudwatch_event_rule", {"Tags": [{"Key": "iacp:team", "Value": "platform"}]}),
        ],
    )
    def test_parse(self, rtype, response):
        assert PROBES[rtype].parse(response) == {"iacp:team": "platform"}

    def test_parse_tolerates_empty_and_malformed(self):
        assert PROBES["aws_sqs_queue"].parse({}) == {}
        assert PROBES["aws_s3_bucket"].parse({"TagSet": [{"Value": "orphan"}, "x"]}) == {}


class TestSmokeTester:
    def test_all_pass(self):
        reader = FakeReader(LIVE_OK)
        result = SmokeTester(reader).run(metadata("development", "platform", "smoke"), PLAN, OUTPUTS, STATE)
        assert result.ok, result.failures
        kinds = [c.kind for c in result.checks]
        assert kinds.count("output") == 1 and kinds.count("state-tags") == 3 and kinds.count("live-tags") == 3
        assert len(reader.calls) == 3
        md = result.render_markdown()
        assert "(passed)" in md and "1 output(s), 3 tagged resource(s) in state, 3 read back from the API." in md

    def test_missing_resource_and_wrong_tags_fail(self):
        live = dict(LIVE_OK)
        del live[("kms", "list-resource-tags", "k-1")]
        live[("sqs", "list-queue-tags", "http://q")] = {}
        result = SmokeTester(FakeReader(live)).run(metadata("development", "platform", "smoke"), PLAN, OUTPUTS, STATE)
        assert not result.ok
        failed = {c.subject: c.detail for c in result.failures}
        assert failed["module.smoke_bucket.aws_kms_key.this[0]"].startswith("could not read the resource")
        assert "iacp:team is missing" in failed["module.smoke_bucket.module.n.aws_sqs_queue.q"]
        md = result.render_markdown()
        assert "(failed)" in md and "- live-tags `module.smoke_bucket.aws_kms_key.this[0]`" in md

    def test_unprobed_types_are_listed_and_checked_in_state(self):
        state = {"values": {"root_module": {"resources": [
            {"address": "aws_lambda_event_source_mapping.m", "mode": "managed",
             "type": "aws_lambda_event_source_mapping", "values": {"tags_all": TAGS}},
        ]}}}
        result = SmokeTester(FakeReader({})).run(metadata("development", "platform", "smoke"), {}, {}, state)
        assert result.ok and result.unprobed == ("aws_lambda_event_source_mapping",)
        assert "state only" in result.render_markdown()
        assert result.to_dict()["unprobed_types"] == ["aws_lambda_event_source_mapping"]

    def test_emulator_untagged_types_are_reported_not_checked(self):
        alarm = {"address": "aws_cloudwatch_metric_alarm.a", "mode": "managed", "type": "aws_cloudwatch_metric_alarm",
                 "values": {"arn": "arn:aws:cloudwatch:us-east-1:0:alarm:a", "tags_all": {}}}
        state = {"values": {"root_module": {"resources": [alarm]}}}
        result = SmokeTester(FakeReader({})).run(metadata("development", "platform", "smoke"), {}, {}, state)
        assert result.ok and result.empty and result.emulator_untagged == ("aws_cloudwatch_metric_alarm",)
        assert "LocalStack does not keep them" in result.render_markdown()
        assert result.to_dict()["emulator_untagged_types"] == ["aws_cloudwatch_metric_alarm"]
        strict = SmokeTester(FakeReader({}), emulator_untagged=frozenset()).run(
            metadata("development", "platform", "smoke"), {}, {}, state)
        assert not strict.ok

    def test_probe_without_identifier_fails(self):
        state = {"values": {"root_module": {"resources": [
            {"address": "aws_lambda_function.f", "mode": "managed", "type": "aws_lambda_function",
             "values": {"tags_all": TAGS}},
        ]}}}
        result = SmokeTester(FakeReader({})).run(metadata("development", "platform", "smoke"), {}, {}, state)
        assert [c.detail for c in result.failures] == ["no arn in state"]

    def test_nothing_to_check_passes(self):
        result = SmokeTester(FakeReader({})).run(metadata("development", "platform", "smoke"), {}, {}, {})
        assert result.ok and result.empty and result.to_dict()["empty"] is True
        md = result.render_markdown()
        assert "(passed, nothing to check)" in md and "Nothing to check" in md and "failed" not in md


class FakeRun:
    def __init__(self, returncode=0, stdout="", stderr="", exc=None):
        self.result = subprocess.CompletedProcess([], returncode, stdout, stderr)
        self.exc = exc
        self.cmd = None

    def __call__(self, cmd, **kwargs):
        self.cmd = cmd
        if self.exc:
            raise self.exc
        return self.result


class TestAwsCliTagReader:
    def test_builds_command_and_parses_json(self):
        run = FakeRun(stdout='{"Tags": {"a": "b"}}')
        body = AwsCliTagReader("http://localhost:4566", "us-east-1", run).read(["sqs", "list-queue-tags", "--queue-url", "u"])
        assert body == {"Tags": {"a": "b"}}
        assert run.cmd == ["aws", "sqs", "list-queue-tags", "--queue-url", "u", "--region", "us-east-1",
                           "--output", "json", "--no-cli-pager", "--endpoint-url", "http://localhost:4566"]

    def test_empty_output(self):
        run = FakeRun(stdout="")
        reader = AwsCliTagReader("http://127.0.0.1:4566", "eu-west-1", run)
        assert reader.read(["sqs", "list-queue-tags", "--queue-url", "u"]) == {}
        assert run.cmd[-2:] == ["--endpoint-url", "http://127.0.0.1:4566"]

    @pytest.mark.parametrize(
        "endpoint",
        [
            "",  # the AWS default endpoint, i.e. real AWS
            "   ",
            "https://s3.us-east-1.amazonaws.com",
            "http://localstack.cloud.evil.example",
            "http://localhost.evil.example:4566",
            "ftp://localhost:4566",
            "localhost:4566",
        ],
    )
    def test_refuses_endpoints_that_could_reach_real_aws(self, endpoint):
        run = FakeRun(stdout="{}")
        with pytest.raises(SmokeConfigError):
            AwsCliTagReader(endpoint, "us-east-1", run)
        assert run.cmd is None

    @pytest.mark.parametrize(
        "endpoint",
        ["http://localhost:4566", "http://127.0.0.1:4566", "http://[::1]:4566",
         "http://s3.localhost.localstack.cloud:4566", "https://LOCALHOST:4566"],
    )
    def test_local_endpoints(self, endpoint):
        assert local_endpoint(endpoint) == endpoint

    @pytest.mark.parametrize(
        "run,message",
        [
            (
                FakeRun(returncode=254, stderr="\nAn error occurred (NoSuchBucket)\n"),
                r"exited 254: An error occurred \(NoSuchBucket\)",
            ),
            (FakeRun(returncode=1), "exited 1: no error output"),
            (FakeRun(stdout="not json"), "invalid JSON"),
            (FakeRun(exc=FileNotFoundError("aws")), "aws"),
            (FakeRun(exc=subprocess.TimeoutExpired("aws", 60)), "timed out"),
        ],
    )
    def test_errors(self, run, message):
        with pytest.raises(SmokeProbeError, match=message):
            AwsCliTagReader(LOCAL, "us-east-1", run).read(["s3api", "get-bucket-tagging", "--bucket", "b"])


class TestSmokeCommand:
    def test_fails_on_missing_inputs(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke")
        argv = ["--repo-root", str(tmp_path), "smoke", str(root), "--plan", str(tmp_path / "p.json"),
                "--outputs", str(tmp_path / "o.json"), "--state", str(tmp_path / "s.json"), "--endpoint-url", LOCAL]
        assert main(argv) == 2
        assert "missing or not a JSON object" in capsys.readouterr().err

    @pytest.mark.parametrize(
        "env_endpoint,message",
        [
            (None, "needs a LocalStack endpoint"),  # no endpoint: the default is an error
            ("https://sts.amazonaws.com", "never calls real AWS"),
        ],
    )
    def test_refuses_to_run_without_a_local_endpoint(self, tmp_path, make_stack, capsys, monkeypatch,
                                                     env_endpoint, message):
        monkeypatch.delenv("AWS_ENDPOINT_URL", raising=False)
        if env_endpoint is not None:
            monkeypatch.setenv("AWS_ENDPOINT_URL", env_endpoint)
        root = make_stack("development", "platform", "smoke")
        for name, doc in (("p", PLAN), ("o", OUTPUTS), ("s", STATE)):
            (tmp_path / f"{name}.json").write_text(json.dumps(doc))
        argv = ["--repo-root", str(tmp_path), "smoke", str(root), "--plan", str(tmp_path / "p.json"),
                "--outputs", str(tmp_path / "o.json"), "--state", str(tmp_path / "s.json")]
        assert main(argv) == 2
        assert message in capsys.readouterr().err

    def test_writes_results_and_exit_code(self, tmp_path, make_stack, capsys, monkeypatch):
        root = make_stack("development", "platform", "smoke")
        for name, doc in (("p", PLAN), ("o", OUTPUTS), ("s", {"values": {"root_module": {}}})):
            (tmp_path / f"{name}.json").write_text(json.dumps(doc))
        md, js = tmp_path / "smoke.md", tmp_path / "smoke.json"
        argv = ["--repo-root", str(tmp_path), "smoke", str(root), "--plan", str(tmp_path / "p.json"),
                "--outputs", str(tmp_path / "o.json"), "--state", str(tmp_path / "s.json"),
                "--markdown-out", str(md), "--json-out", str(js), "--endpoint-url", LOCAL]
        assert main(argv) == 0
        assert json.loads(js.read_text())["ok"] is True and "(passed)" in md.read_text()
        (tmp_path / "o.json").write_text("{}")
        assert main(argv) == 1
        out = capsys.readouterr().out
        assert "FAIL output smoke_bucket: missing from terraform output" in out
        assert "::error title=Smoke test failed::" in out
