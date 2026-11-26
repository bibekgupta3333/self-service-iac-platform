import json
from pathlib import Path

import pytest

from tools.required_checks.checks import RepositoryChecks, job_names, problems, required_contexts
from tools.required_checks.cli import main

REPO_ROOT = Path(__file__).resolve().parents[3]

RULESET = {
    "rules": [
        {"type": "deletion"},
        {"type": "required_status_checks",
         "parameters": {"required_status_checks": [{"context": "a"}, {"context": "b (x)"}]}},
    ]
}


def test_required_contexts():
    assert required_contexts(RULESET) == ("a", "b (x)")
    assert required_contexts({}) == ()
    assert required_contexts({"rules": ["x", {"type": "required_status_checks"}]}) == ()


def test_job_names_use_the_name_or_the_id():
    assert job_names({"jobs": {"one": {"name": "First"}, "two": {"runs-on": "x"}, "bad": None}}) == ("First", "two", "bad")


def test_problems():
    assert problems(("a", "b"), frozenset({"a", "b"})) == ()
    assert "'c' is not the name of any job" in problems(("c",), frozenset({"a"}))[0]
    assert "more than once" in problems(("a", "a"), frozenset({"a"}))[0]
    assert "matrix" in problems(("v ${{ m }}",), frozenset({"v ${{ m }}"}))[0]


def test_the_repository_ruleset_names_real_jobs():
    assert RepositoryChecks(REPO_ROOT).problems() == ()


@pytest.mark.parametrize(
    "job",
    [
        "module tests (terraform test, mocked providers)",
        "terratest (LocalStack)",
        "policy (opa, conftest, checkov checks)",
        "portal (lint, typecheck, test, build)",
        "pr-validate-result",
    ],
)
def test_the_ruleset_requires(job):
    assert job in RepositoryChecks(REPO_ROOT).contexts()


def test_cli(tmp_path, capsys):
    (tmp_path / ".github/rulesets").mkdir(parents=True)
    (tmp_path / ".github/workflows").mkdir()
    (tmp_path / ".github/rulesets/main-branch.json").write_text(json.dumps(RULESET))
    (tmp_path / ".github/workflows/w.yml").write_text("jobs:\n  a:\n    name: a\n")
    assert main(["--repo-root", str(tmp_path), "--check"]) == 1
    assert "'b (x)' is not the name of any job" in capsys.readouterr().err
    assert main(["--repo-root", str(tmp_path)]) == 0
    assert main(["--repo-root", str(tmp_path / "missing"), "--check"]) == 2
