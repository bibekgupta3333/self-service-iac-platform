"""ReleaseGate, the git/workspace adapters and the CLI against a real temporary git repository."""

from __future__ import annotations

import json

import pytest

from tools.module_release import Bump, GitError, GitRepository, ModuleWorkspace, ReleaseGate
from tools.module_release.cli import main

from .conftest import OUTPUTS_TF, REPO_ROOT, VARIABLES_TF

NEW_REQUIRED = VARIABLES_TF + '\nvariable "region" {\n  type = string\n}\n'
NEW_OPTIONAL = VARIABLES_TF + '\nvariable "region" {\n  type    = string\n  default = "us-east-1"\n}\n'
NEW_OUTPUT = OUTPUTS_TF + '\noutput "arn" {\n  value = "x"\n}\n'


def released(repo, version: str = "1.0.0"):
    """A repo with module demo committed and tagged at ``version``."""
    repo.add_module(version=version)
    repo.commit("release")
    repo.tag(f"modules/demo/v{version}")
    return repo


def check(repo, module: str = "demo"):
    return ReleaseGate(ModuleWorkspace(repo.root), GitRepository(repo.root)).check(module)


class TestReleaseGate:
    def test_first_release(self, repo):
        repo.add_module()
        repo.commit()
        result = check(repo)
        assert result.ok and result.first_release and result.releasable

    def test_unchanged_since_tag(self, released_repo):
        result = check(released_repo)
        assert result.ok and not result.releasable and result.changes == ()

    def test_breaking_change_with_minor_bump_fails(self, released_repo):
        released_repo.write("modules/demo/variables.tf", NEW_REQUIRED)
        released_repo.set_version("demo", "1.1.0")
        result = check(released_repo)
        assert not result.ok and result.required is Bump.MAJOR
        assert [c.name for c in result.changes] == ["region"]

    def test_breaking_change_with_major_bump_passes(self, released_repo):
        released_repo.write("modules/demo/variables.tf", NEW_REQUIRED)
        released_repo.set_version("demo", "2.0.0")
        released_repo.commit()
        assert check(released_repo).ok

    def test_removed_output_needs_major(self, released_repo):
        released_repo.write("modules/demo/outputs.tf", "")
        released_repo.set_version("demo", "1.0.1")
        assert check(released_repo).required is Bump.MAJOR

    @pytest.mark.parametrize("path,text", [("modules/demo/variables.tf", NEW_OPTIONAL),
                                           ("modules/demo/outputs.tf", NEW_OUTPUT)])
    def test_additions_need_minor(self, released_repo, path, text):
        released_repo.write(path, text)
        released_repo.set_version("demo", "1.0.1")
        result = check(released_repo)
        assert not result.ok and result.required is Bump.MINOR
        released_repo.set_version("demo", "1.1.0")
        assert check(released_repo).ok

    def test_non_interface_change_without_bump_fails(self, released_repo):
        released_repo.write("modules/demo/main.tf", "locals {\n  x = 1\n}\n")
        result = check(released_repo)
        assert not result.ok and result.required is Bump.PATCH and "not bumped" in result.errors[0]

    def test_untracked_file_counts_as_change(self, released_repo):
        released_repo.write("modules/demo/new.tf", "")
        assert not check(released_repo).ok

    def test_tests_and_examples_do_not_require_a_bump(self, released_repo):
        released_repo.write("modules/demo/tests/basic.tftest.hcl", "run \"x\" {}\n")
        released_repo.write("modules/demo/examples/basic/main.tf", "# example\n")
        released_repo.commit()
        assert check(released_repo).ok

    def test_compares_against_highest_final_tag(self, released_repo):
        released_repo.write("modules/demo/variables.tf", NEW_OPTIONAL)
        released_repo.set_version("demo", "1.1.0")
        released_repo.commit()
        released_repo.tag("modules/demo/v1.1.0")
        released_repo.tag("modules/demo/v2.0.0-rc.1")
        result = check(released_repo)
        assert str(result.previous) == "1.1.0" and result.ok and not result.releasable

    def test_unparsable_hcl_is_an_error(self, released_repo):
        released_repo.write("modules/demo/variables.tf", 'variable "name" {')
        released_repo.set_version("demo", "1.0.1")
        result = check(released_repo)
        assert not result.ok and "cannot parse HCL" in result.errors[0]

    def test_missing_version(self, repo):
        repo.add_module()
        repo.write("modules/demo/module.yaml", "metadata:\n  name: demo\n")
        with pytest.raises(ValueError, match="metadata.version"):
            check(repo)


class TestAdapters:
    def test_files_at_missing_ref_or_dir(self, released_repo):
        git = GitRepository(released_repo.root)
        assert set(git.files_at("modules/demo/v1.0.0", "modules/demo")) == {
            "CHANGELOG.md", "module.yaml", "outputs.tf", "variables.tf"}
        assert git.files_at("modules/demo/v1.0.0", "modules/nope") == {}

    def test_has_ref_and_errors(self, released_repo):
        git = GitRepository(released_repo.root)
        assert git.has_ref("HEAD") and not git.has_ref("deadbeef")
        with pytest.raises(GitError):
            git.changed_paths("no-such-ref")

    def test_workspace(self, repo):
        repo.add_module("b")
        repo.add_module("a", changelog=None)
        repo.write("modules/notamodule/README.md", "x")
        ws = ModuleWorkspace(repo.root)
        assert ws.module_names() == ["a", "b"]
        assert ws.changelog("a") is None and "Initial release" in ws.changelog("b")
        with pytest.raises(FileNotFoundError):
            ModuleWorkspace(repo.root / "nope").module_names()
        with pytest.raises(FileNotFoundError):
            ws.manifest("notamodule")
        repo.write("modules/a/module.yaml", "- a list\n")
        with pytest.raises(ValueError, match="mapping"):
            ws.manifest("a")


@pytest.fixture
def released_repo(repo):
    return released(repo)


def run(repo, *argv, env=None):
    return main(["--repo", str(repo.root), *argv], env=env)


class TestCli:
    def test_gate_text_and_exit_codes(self, released_repo, capsys):
        assert run(released_repo, "gate", "demo") == 0
        released_repo.write("modules/demo/variables.tf", NEW_REQUIRED)
        released_repo.set_version("demo", "1.1.0")
        assert run(released_repo, "gate", "--all") == 1
        out = capsys.readouterr().out
        assert "FAIL demo 1.1.0" in out and "[major] variable 'region'" in out and "1 failed" in out

    def test_gate_json(self, released_repo, capsys):
        assert run(released_repo, "gate", "demo", "--format", "json") == 0
        data = json.loads(capsys.readouterr().out)
        assert data[0]["module"] == "demo" and data[0]["previous"] == "1.0.0" and data[0]["releasable"] is False

    def test_gate_usage_errors(self, released_repo, capsys):
        assert run(released_repo, "gate") == 2
        assert run(released_repo, "gate", "nope") == 2
        assert "unknown module" in capsys.readouterr().err

    def test_detect(self, released_repo, capsys):
        released_repo.add_module("other")
        base = released_repo.git("rev-parse", "HEAD")
        released_repo.commit("add other")
        released_repo.write("modules/demo/tests/x.tftest.hcl", "")
        released_repo.write("README.md", "x")
        head = released_repo.commit("touch demo tests")
        assert run(released_repo, "detect", "--base", base, "--head", head) == 0
        assert json.loads(capsys.readouterr().out) == ["demo", "other"]
        assert run(released_repo, "detect", "--base", "HEAD~1") == 0
        assert json.loads(capsys.readouterr().out) == ["demo"]

    def test_detect_unknown_base_means_all_and_untagged(self, released_repo, capsys):
        released_repo.add_module("other")
        released_repo.commit()
        assert run(released_repo, "detect", "--base", "0" * 40) == 0
        assert json.loads(capsys.readouterr().out) == ["demo", "other"]
        assert run(released_repo, "detect", "--base", "HEAD", "--include-untagged") == 0
        assert json.loads(capsys.readouterr().out) == ["other"]

    def test_info(self, released_repo, capsys):
        assert run(released_repo, "info", "demo") == 0
        out = capsys.readouterr().out.splitlines()
        assert out == ["module=demo", "version=1.0.0", "tag=modules/demo/v1.0.0", "prerelease=false",
                       "tag_exists=true"]

    def test_notes_from_changelog_and_fallback(self, released_repo, capsys):
        released_repo.write("modules/demo/outputs.tf", NEW_OUTPUT)
        released_repo.set_version("demo", "1.1.0")
        assert run(released_repo, "notes", "demo") == 0
        out = capsys.readouterr().out
        assert "Previous release: 1.0.0" in out and "[minor] output 'arn': added" in out
        released_repo.set_version("demo", "1.0.0")
        released_repo.write("modules/demo/outputs.tf", OUTPUTS_TF)
        assert run(released_repo, "notes", "demo") == 0
        assert capsys.readouterr().out.strip() == "### Added\n- Initial release."

    def test_index_rejects_bad_sha(self, released_repo, capsys):
        assert run(released_repo, "index", "demo", "--commit-sha", "HEAD") == 2
        assert "40-character" in capsys.readouterr().err

    def test_index_dry_run(self, released_repo, capsys):
        sha = released_repo.git("rev-parse", "HEAD")
        assert run(released_repo, "index", "demo", "--commit-sha", sha, "--dry-run", env={"GITHUB_ACTOR": "bot"}) == 0
        payload = json.loads(capsys.readouterr().out)
        assert payload["git_tag"] == "modules/demo/v1.0.0" and payload["released_by"] == "bot"
        assert payload["changelog_md"] == "### Added\n- Initial release."

    def test_index_unconfigured(self, released_repo, capsys):
        sha = released_repo.git("rev-parse", "HEAD")
        assert run(released_repo, "index", "demo", "--commit-sha", sha, "--skip-if-unconfigured", env={}) == 0
        assert "::notice" in capsys.readouterr().out
        assert run(released_repo, "index", "demo", "--commit-sha", sha, env={"IACP_API_URL": "http://localhost"}) == 2
        assert "IACP_INTERNAL_TOKEN must be set" in capsys.readouterr().err


def test_repository_modules_pass_the_gate():
    """The modules in this repository must pass against their own tags (first release before any tag)."""
    gate = ReleaseGate(ModuleWorkspace(REPO_ROOT), GitRepository(REPO_ROOT))
    results = [gate.check(m) for m in ModuleWorkspace(REPO_ROOT).module_names()]
    assert results and all(r.ok for r in results), [r.errors for r in results]
