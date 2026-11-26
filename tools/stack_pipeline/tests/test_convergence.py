import json

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.convergence import residual_changes

ALARM = "aws_cloudwatch_metric_alarm"


def rc(address, rtype, actions, before=None, after=None):
    return {"address": address, "type": rtype, "change": {"actions": actions, "before": before, "after": after}}


def plan(*changes):
    return {"resource_changes": list(changes)}


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

    def test_strict_mode_counts_everything(self):
        change = rc("x.alarm", ALARM, ["update"], {"tags": {}}, {"tags": TAGS})
        assert residual_changes(plan(change), emulator_untagged=frozenset()) == ("x.alarm: update",)


class TestConvergedCommand:
    def run(self, tmp_path, doc, capsys):
        path = tmp_path / "replan.json"
        path.write_text(json.dumps(doc))
        code = main(["--repo-root", str(tmp_path), "converged", "--plan", str(path)])
        return code, capsys.readouterr().out

    def test_exit_codes(self, tmp_path, capsys):
        code, out = self.run(tmp_path, plan(), capsys)
        assert code == 0 and "converged" in out
        code, out = self.run(tmp_path, plan(rc("x.q", "aws_sqs_queue", ["update"], {"a": 1}, {"a": 2})), capsys)
        assert code == 1
        assert "not converged: x.q: update" in out and "::error title=Stack is not idempotent::" in out
