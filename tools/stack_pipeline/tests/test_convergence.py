import json

from tools.stack_pipeline.cli import main
import re
from pathlib import Path

from tools.stack_pipeline.convergence import residual_changes
from tools.stack_pipeline.smoke import EMULATOR_UNTAGGED

ALARM = "aws_cloudwatch_metric_alarm"


def rc(address, rtype, actions, before=None, after=None):
    return {"address": address, "type": rtype, "change": {"actions": actions, "before": before, "after": after}}


def plan(*changes):
    return {"format_version": "1.2", "resource_changes": list(changes)}


TAGS = {"iacp:team": "platform"}


class TestResidualChanges:
    def test_empty_replan_is_converged(self):
        assert residual_changes(plan()) == ()
        assert residual_changes({}) == ()
        assert residual_changes(plan(rc("a.b", "aws_s3_bucket", ["no-op"]), rc("d.e", "aws_region", ["read"]))) == ()

    def test_emulator_tag_drop_on_an_alarm_is_not_counted(self):
        change = rc("module.m.aws_cloudwatch_metric_alarm.this[\"a\"]", ALARM, ["update"],
                    {"alarm_name": "a", "tags": {}, "tags_all": {}}, {"alarm_name": "a", "tags": TAGS, "tags_all": TAGS})
        assert residual_changes(plan(change)) == ()

    def test_other_attribute_change_on_an_alarm_is_counted(self):
        change = rc("x.alarm", ALARM, ["update"], {"threshold": 1, "tags": {}}, {"threshold": 2, "tags": TAGS})
        assert residual_changes(plan(change)) == ("x.alarm: update",)

    def test_tag_update_on_other_types_is_counted(self):
        change = rc("x.bucket", "aws_s3_bucket", ["update"], {"tags": {}}, {"tags": TAGS})
        assert residual_changes(plan(change)) == ("x.bucket: update",)

    def test_replace_and_create_on_an_alarm_are_counted(self):
        assert residual_changes(plan(rc("x.alarm", ALARM, ["delete", "create"]))) == ("x.alarm: delete+create",)
        assert residual_changes(plan(rc("x.alarm", ALARM, ["create"]))) == ("x.alarm: create",)

    def test_output_changes_are_counted(self):
        doc = {"format_version": "1.2", "resource_changes": [], "output_changes": {
            "a": {"actions": ["no-op"]}, "b": {"actions": ["update"]}, "c": {"actions": ["create"]}}}
        assert residual_changes(doc) == ("output b: update", "output c: create")

    def test_alarm_tag_update_with_another_unknown_attribute_is_counted(self):
        change = rc("x.alarm", ALARM, ["update"], {"alarm_name": "a", "tags": {}}, {"alarm_name": "a", "tags": TAGS})
        change["change"]["after_unknown"] = {"tags_all": True, "arn": True}
        assert residual_changes(plan(change)) == ("x.alarm: update",)
        change["change"]["after_unknown"] = {"tags_all": True, "arn": False}
        assert residual_changes(plan(change)) == ()

    def test_strict_mode_counts_everything(self):
        change = rc("x.alarm", ALARM, ["update"], {"tags": {}}, {"tags": TAGS})
        assert residual_changes(plan(change), emulator_untagged=frozenset()) == ("x.alarm: update",)


class TestConvergedCommand:
    def run(self, tmp_path, doc, capsys):
        path = tmp_path / "replan.json"
        path.write_text(json.dumps(doc))
        code = main(["--repo-root", str(tmp_path), "converged", "--plan", str(path)])
        return code, capsys.readouterr().out

    def test_refuses_documents_that_are_not_a_usable_plan(self, tmp_path, capsys):
        for doc in ({"resource_changes": []}, dict(plan(), errored=True)):
            path = tmp_path / "bad.json"
            path.write_text(json.dumps(doc))
            assert main(["--repo-root", str(tmp_path), "converged", "--plan", str(path)]) == 2
        err = capsys.readouterr().err
        assert "no format_version" in err and "the plan errored" in err

    def test_exit_codes(self, tmp_path, capsys):
        code, out = self.run(tmp_path, plan(), capsys)
        assert code == 0 and "converged" in out
        code, out = self.run(tmp_path, plan(rc("x.q", "aws_sqs_queue", ["update"], {"a": 1}, {"a": 2})), capsys)
        assert code == 1
        assert "not converged: x.q: update" in out and "::error title=Stack is not idempotent::" in out


REPO = Path(__file__).resolve().parents[3]


def test_emulator_exceptions_are_tied_to_the_pinned_localstack_image():
    """EMULATOR_UNTAGGED describes LocalStack 4.4. When the image pin moves, re-test
    whether PutMetricAlarm keeps tags and empty the set by hand if it does."""
    pins = {
        path.name: set(re.findall(r"localstack/localstack:(\d+\.\d+)", path.read_text(encoding="utf-8")))
        for path in [REPO / "docker-compose.yml", *(REPO / ".github" / "workflows").glob("*.yml")]
    }
    seen = set().union(*pins.values())
    assert EMULATOR_UNTAGGED and seen <= {"4.4"}, f"LocalStack pin changed to {seen}: revisit EMULATOR_UNTAGGED"
