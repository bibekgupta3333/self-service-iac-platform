"""The ci.yml SARIF strip must fail closed: strip into its own directory, upload only that directory."""

import json
import re
import shutil
import subprocess
from pathlib import Path

import pytest
import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
STRIP = "Strip run properties the upload owns"
KEEP = "Keep SARIF for the code scanning upload"


@pytest.fixture(scope="module")
def steps():
    workflow = yaml.safe_load((REPO_ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8"))
    return {s["name"]: s for s in workflow["jobs"]["terraform"]["steps"] if "name" in s}


def test_the_strip_step_can_fail_the_job(steps):
    assert "continue-on-error" not in steps[STRIP]


def test_only_the_stripped_directory_is_uploaded(steps):
    path = steps[KEEP]["with"]["path"]
    assert "sarif-upload" in path and not re.search(r"/sarif/", path)
    # The strip step writes there and never moves a raw file into it unprocessed.
    assert "$RUNNER_TEMP/sarif-upload" in steps[STRIP]["run"]
    assert re.search(r"jq '\.runs \|= map\(del\(", steps[STRIP]["run"])


def test_the_strip_step_installs_nothing_from_the_checkout(steps):
    run = steps[STRIP]["run"]
    for forbidden in ("pip install", "tools/requirements", "python "):
        assert forbidden not in run


@pytest.mark.skipif(shutil.which("jq") is None, reason="jq is not installed")
def test_the_jq_filter_drops_forged_run_properties(steps, tmp_path):
    filt = re.search(r"jq '(\.runs \|= map\(del\([^']*\)\))'", steps[STRIP]["run"]).group(1)
    doc = {"runs": [{"automationDetails": {"id": "ci/other/"}, "versionControlProvenance": [{"repositoryUri": "x"}],
                     "tool": {"driver": {"name": "t"}}, "results": []}]}
    src = tmp_path / "in.sarif"
    src.write_text(json.dumps(doc))
    out = subprocess.run(["jq", filt, str(src)], check=True, capture_output=True, text=True).stdout
    assert json.loads(out) == {"runs": [{"tool": {"driver": {"name": "t"}}, "results": []}]}
