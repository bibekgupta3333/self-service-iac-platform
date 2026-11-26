from __future__ import annotations

import io
import json
import subprocess
from pathlib import Path
from typing import Any

import pytest
import yaml

from tools.codeowners.adapters import stack_files
from tools.codeowners.cli import main
from tools.codeowners.models import ConfigError, OwnersConfig, Team
from tools.codeowners.render import drift, render, team_rules, undeclared_teams

REPO_ROOT = Path(__file__).resolve().parents[3]


def config(**overrides: Any) -> dict[str, Any]:
    doc: dict[str, Any] = {
        "org": "acme",
        "default_owners": ["@acme/platform"],
        "platform_owners": ["@acme/platform-security"],
        "teams": {"payments": None, "search": {"owners": ["@acme/search-devs"]}},
    }
    doc.update(overrides)
    return doc


# --- models -----------------------------------------------------------------------


def test_team_owner_resolution() -> None:
    t = Team.from_yaml("payments", {"owners": ["@acme/pay"], "environments": {"production": ["@acme/pay-maintainers"]}})
    assert t.owners_for("development", "acme") == ("@acme/pay",)
    assert t.owners_for("production", "acme") == ("@acme/pay-maintainers",)
    assert Team.from_yaml("search", None).owners_for("staging", "acme") == ("@acme/search",)


def test_team_without_owners_needs_an_org() -> None:
    with pytest.raises(ConfigError, match="no org"):
        OwnersConfig.from_yaml(config(org=None))


@pytest.mark.parametrize(
    ("doc", "message"),
    [
        (config(teams={"Payments": None}), "team slug"),
        (config(teams={"payments": {"owners": ["payments"]}}), "invalid owner"),
        (config(teams={"payments": {"owners": []}}), "non-empty"),
        (config(teams={"payments": {"environments": {"qa": ["@a"]}}}), "unknown environment"),
        (config(teams={"payments": {"reviewers": ["@a"]}}), "unknown keys"),
        (config(default_owners=None), "default_owners"),
        (config(platform_paths=["modules/"]), "platform_paths"),
        (config(org="bad org"), "org"),
        (config(platform_paths=["/environments/", "/environments/production/"]), "under /environments/<env>/"),
        (config(platform_paths=["/environments/staging/payments/"]), "under /environments/<env>/"),
        (config(extra=1), "unknown top-level"),
        ([], "mapping"),
    ],
)
def test_invalid_config_rejected(doc: Any, message: str) -> None:
    with pytest.raises(ConfigError, match=message):
        OwnersConfig.from_yaml(doc)


def test_email_owner_accepted() -> None:
    assert Team.from_yaml("ops", {"owners": ["ops@example.com"]}).owners == ("ops@example.com",)


# --- render ------------------------------------------------------------------------


def test_render_orders_team_rules_after_platform_paths() -> None:
    text = render(OwnersConfig.from_yaml(config()))
    lines = [ln for ln in text.splitlines() if ln and not ln.startswith("#")]
    patterns = [ln.split()[0] for ln in lines]
    assert patterns[0] == "*"
    assert patterns.index("/environments/") < patterns.index("/environments/development/payments/")
    assert "/environments/production/search/            @acme/search-devs".split() == lines[-1].split()
    assert "/environments/staging/payments/ @acme/payments".split() in [ln.split() for ln in lines]
    assert text.startswith("# GENERATED")
    assert text.endswith("\n") and not text.endswith("\n\n")


def test_render_is_deterministic_regardless_of_team_order() -> None:
    a = render(OwnersConfig.from_yaml(config(teams={"b-team": None, "a-team": None})))
    b = render(OwnersConfig.from_yaml(config(teams={"a-team": None, "b-team": None})))
    assert a == b
    assert [r.pattern for r in team_rules(OwnersConfig.from_yaml(config(teams={"b-team": None, "a-team": None})))][0] == (
        "/environments/development/a-team/"
    )


def test_render_without_teams_has_no_team_section() -> None:
    assert "Team stack roots" not in render(OwnersConfig.from_yaml(config(teams={})))


def test_undeclared_teams() -> None:
    cfg = OwnersConfig.from_yaml(config())
    assert undeclared_teams(cfg, {"payments", "data", "analytics"}) == ["analytics", "data"]


def test_drift() -> None:
    assert drift("a\n", "a\n", "X") == []
    out = "".join(drift("a\nb\n", "a\n", "X"))
    assert "+b" in out and "X (committed)" in out
    assert drift("a\n", None, "X")  # missing file is drift


# --- CLI and adapters -----------------------------------------------------------------


@pytest.fixture
def repo(tmp_path: Path) -> Path:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    (tmp_path / ".github").mkdir()
    (tmp_path / ".github" / "teams.yaml").write_text(yaml.safe_dump(config()))
    return tmp_path


def add_stack(root: Path, env: str, team: str, stack: str, declared: str | None = None, track: bool = True) -> None:
    d = root / "environments" / env / team / stack
    d.mkdir(parents=True)
    (d / "iacp.stack.json").write_text(json.dumps({"team": declared or team, "stack": stack, "environment": env}))
    if track:
        subprocess.run(["git", "add", "--", str(d / "iacp.stack.json")], cwd=root, check=True)


def run(root: Path, *args: str) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    code = main(["--repo-root", str(root), *args], out=out, err=err)
    return code, out.getvalue(), err.getvalue()


def test_write_then_check(repo: Path) -> None:
    add_stack(repo, "development", "payments", "orders")
    assert run(repo, "--check")[0] == 1  # nothing written yet
    code, out, _ = run(repo)
    assert code == 0 and "wrote" in out
    assert run(repo, "--check")[0] == 0
    assert "already up to date" in run(repo)[1]


def test_check_reports_hand_edits(repo: Path) -> None:
    run(repo)
    path = repo / ".github" / "CODEOWNERS"
    path.write_text(path.read_text() + "/secret/ @someone\n")
    code, _, err = run(repo, "--check")
    assert code == 1 and "-/secret/ @someone" in err and "out of date" in err


def test_stack_of_undeclared_team_fails(repo: Path) -> None:
    add_stack(repo, "staging", "data", "lake")
    code, _, err = run(repo, "--check")
    assert code == 1 and "data" in err


def test_untracked_stacks_are_ignored(repo: Path) -> None:
    add_stack(repo, "staging", "data", "lake", track=False)  # leftover, never committed
    run(repo)
    assert run(repo, "--check")[0] == 0


def test_stack_files() -> None:
    paths = [
        "environments/production/payments/orders/iacp.stack.json",
        "environments/production/payments/orders/main.tf",
        "environments/qa/payments/orders/iacp.stack.json",
        "environments/production/payments/iacp.stack.json",
        "environments/development/search/idx/iacp.stack.json",
    ]
    assert stack_files(paths) == [
        ("environments/development/search/idx/iacp.stack.json", "search"),
        ("environments/production/payments/orders/iacp.stack.json", "payments"),
    ]


def test_stack_metadata_must_match_its_directory(repo: Path) -> None:
    add_stack(repo, "staging", "payments", "orders", declared="search")
    code, _, err = run(repo)
    assert code == 2 and "disagrees" in err


def test_bad_config_exit_code(tmp_path: Path) -> None:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    code, _, err = run(tmp_path)
    assert code == 2 and "not found" in err
    (tmp_path / ".github").mkdir()
    (tmp_path / ".github" / "teams.yaml").write_text("org: [unclosed")
    assert run(tmp_path)[0] == 2


def test_committed_codeowners_is_current() -> None:
    """The repository's own CODEOWNERS matches its teams.yaml (same as the CI check)."""
    assert run(REPO_ROOT, "--check")[0] == 0
