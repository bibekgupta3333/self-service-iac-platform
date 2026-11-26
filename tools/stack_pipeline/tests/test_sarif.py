import json

import pytest

from tools.stack_pipeline.cli import main
from tools.stack_pipeline.sarif import SarifError, rebase_sarif, rebase_uri, result_count

STACK = "environments/development/platform/smoke"
DL = "/home/runner/work/_temp/checkov-modules"
MODULE_FILE = (
    "home/runner/work/_temp/checkov-modules/github.com/bibekgupta3333/self-service-iac-platform/"
    "modules/s3/v1.0.0/modules/s3/kms.tf"
)


def sarif(*uris, tool="Checkov"):
    results = [
        {"ruleId": "R", "locations": [{"physicalLocation": {"artifactLocation": {"uri": u}, "region": {"startLine": 3}}}]}
        for u in uris
    ]
    return {"version": "2.1.0", "runs": [{"tool": {"driver": {"name": tool}}, "results": results}]}


class TestRebaseUri:
    @pytest.mark.parametrize(
        "uri,expected",
        [
            ("main.tf", f"{STACK}/main.tf"),
            ("./outputs.tf", f"{STACK}/outputs.tf"),
            ("file://main.tf", f"{STACK}/main.tf"),
            # Checkov prints absolute paths without the leading slash; both spellings work.
            (MODULE_FILE, "modules/s3/kms.tf"),
            ("/" + MODULE_FILE, "modules/s3/kms.tf"),
            ("file:///" + MODULE_FILE, "modules/s3/kms.tf"),
            # Under the download dir but not shaped like a pinned module: the stack's main.tf.
            ("home/runner/work/_temp/checkov-modules/registry/x.tf", f"{STACK}/main.tf"),
            # Absolute paths in the checkout become repository-relative.
            (f"/home/runner/work/repo/repo/{STACK}/versions.tf", f"{STACK}/versions.tf"),
            # Never outside the repository or the stack.
            ("../../../../../etc/passwd", f"{STACK}/main.tf"),
            ("/etc/passwd", f"{STACK}/main.tf"),
            ("https://example.com/x.tf", f"{STACK}/main.tf"),
            ("", f"{STACK}/main.tf"),
            (".", f"{STACK}/main.tf"),
            ("../other/main.tf", f"{STACK}/main.tf"),
        ],
    )
    def test_rebase(self, uri, expected):
        assert rebase_uri(uri, STACK, [DL], repo_root="/home/runner/work/repo/repo") == expected

    def test_prerelease_tag(self):
        uri = f"{DL}/x/modules/sqs/v2.0.0-rc.1/modules/sqs/main.tf"
        assert rebase_uri(uri, STACK, [DL]) == "modules/sqs/main.tf"


class TestRebaseSarif:
    def test_rewrites_results_and_artifacts_without_mutating_input(self):
        doc = sarif("main.tf", MODULE_FILE)
        doc["runs"][0]["artifacts"] = [{"location": {"uri": "main.tf", "uriBaseId": "SRCROOT"}}]
        doc["runs"][0]["results"][0]["locations"][0]["physicalLocation"]["artifactLocation"]["uriBaseId"] = "SRCROOT"
        original = json.dumps(doc, sort_keys=True)
        out = rebase_sarif(doc, STACK, [DL])
        assert json.dumps(doc, sort_keys=True) == original
        locs = [r["locations"][0]["physicalLocation"] for r in out["runs"][0]["results"]]
        assert [loc["artifactLocation"] for loc in locs] == [{"uri": f"{STACK}/main.tf"}, {"uri": "modules/s3/kms.tf"}]
        assert locs[0]["region"] == {"startLine": 3}
        assert out["runs"][0]["artifacts"] == [{"location": {"uri": f"{STACK}/main.tf"}}]
        assert result_count(out) == 2

    def test_related_locations_and_empty_runs(self):
        doc = sarif()
        doc["runs"][0]["results"] = [{"relatedLocations": [{"physicalLocation": {"artifactLocation": {"uri": "a.tf"}}}]}]
        doc["runs"].append({"tool": {"driver": {"name": "tflint-errors"}}, "results": []})
        out = rebase_sarif(doc, STACK)
        assert out["runs"][0]["results"][0]["relatedLocations"][0]["physicalLocation"]["artifactLocation"]["uri"] == (
            f"{STACK}/a.tf"
        )
        assert result_count(out) == 1

    @pytest.mark.parametrize("doc", [None, [], {"version": "2.1.0"}, {"runs": {}}])
    def test_not_sarif(self, doc):
        with pytest.raises(SarifError):
            rebase_sarif(doc, STACK)


class TestSarifCommand:
    def test_writes_repo_relative_sarif(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke")
        dl = tmp_path / "dl"
        module_file = dl / "github.com/o/r/modules/s3/v1.0.0/modules/s3/kms.tf"
        src, out = tmp_path / "in.sarif", tmp_path / "out.sarif"
        src.write_text(json.dumps(sarif("main.tf", str(module_file).lstrip("/"), str(root / "outputs.tf"))))
        argv = ["--repo-root", str(tmp_path), "sarif", str(root), "--in", str(src), "--out", str(out)]
        assert main([*argv, "--external-dir", str(dl)]) == 0
        results = json.loads(out.read_text())["runs"][0]["results"]
        uris = [r["locations"][0]["physicalLocation"]["artifactLocation"]["uri"] for r in results]
        assert uris == [f"{STACK}/main.tf", "modules/s3/kms.tf", f"{STACK}/outputs.tf"]
        assert "3 SARIF result(s)" in capsys.readouterr().out

    def test_missing_input_is_a_usage_error(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke")
        argv = ["--repo-root", str(tmp_path), "sarif", str(root), "--in", str(tmp_path / "nope"), "--out", str(tmp_path / "o")]
        assert main(argv) == 2
        assert "missing" in capsys.readouterr().err
