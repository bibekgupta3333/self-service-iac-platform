import os
import subprocess
from pathlib import Path

import pytest

from tools.stack_pipeline.adapters import GitCli
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.models import ModulePin
from tools.stack_pipeline.module_pins import (
    ModulePinVerifier,
    PinError,
    Resolution,
    insteadof_config,
    manifest_version,
    parse_tag,
    unique_pins,
)

from .conftest import metadata

SHA = "a" * 40
MANIFEST_V1 = "apiVersion: iacp.dev/v1\nmetadata:\n  name: s3\n  version: 1.0.0\n"


class FakeGit:
    def __init__(self, published=None, commits=(SHA,), on_main=(SHA,), files=None):
        self.published = published or {}
        self.commits = set(commits)
        self.on_main = set(on_main)
        self.files = files if files is not None else {(SHA, "modules/s3/module.yaml"): MANIFEST_V1}
        self.pushed = []

    def remote_tag_commit(self, tag):
        return self.published.get(tag)

    def is_commit(self, sha):
        return sha in self.commits

    def is_ancestor(self, sha, ref):
        return sha in self.on_main

    def show(self, sha, path):
        return self.files.get((sha, path))

    def publish_tag(self, mirror, sha, tag):
        self.pushed.append((sha, tag))


class TestPure:
    def test_parse_tag(self):
        assert parse_tag("modules/s3/v1.0.0") == ("s3", "1.0.0")

    @pytest.mark.parametrize("ref", ["main", "modules/s3/v1.0", "modules/S3/v1.0.0", "v1.0.0", "modules/s3/1.0.0"])
    def test_parse_tag_rejects(self, ref):
        with pytest.raises(PinError):
            parse_tag(ref)

    @pytest.mark.parametrize(
        "text,expected",
        [(MANIFEST_V1, "1.0.0"), ("metadata: {}", None), ("- a list", None), (": : bad", None), ("", None)],
    )
    def test_manifest_version(self, text, expected):
        assert manifest_version(text) == expected

    def test_unique_pins_order(self):
        stacks = [metadata("development", "platform", "smoke", modules={"z": {"ref": "r", "sha": "s"}, "a": {"ref": "r2", "sha": "s2"}})]
        assert [(s, i) for s, i, _ in unique_pins(stacks)] == [("development/platform/smoke", "a"), ("development/platform/smoke", "z")]

    def test_insteadof_config_rewrites_both_spellings(self, tmp_path):
        env = insteadof_config(tmp_path / "m.git", "https://github.com/o/r.git")
        assert env["GIT_CONFIG_COUNT"] == "2"
        assert {env["GIT_CONFIG_VALUE_0"], env["GIT_CONFIG_VALUE_1"]} == {"https://github.com/o/r", "https://github.com/o/r.git"}
        assert env["GIT_CONFIG_KEY_0"] == f"url.{(tmp_path / 'm.git').resolve().as_uri()}.insteadOf"


class TestVerifier:
    def check(self, git, ref="modules/s3/v1.0.0", sha=SHA):
        return ModulePinVerifier(git, Path("/tmp/mirror"), allow_unpublished=True).check("development/platform/smoke", "bucket", ModulePin(ref, sha))

    def test_published_tag_matching_sha(self):
        git = FakeGit(published={"modules/s3/v1.0.0": SHA})
        assert self.check(git).resolution is Resolution.PUBLISHED
        assert git.pushed == []

    def test_published_tag_with_other_sha_fails(self):
        git = FakeGit(published={"modules/s3/v1.0.0": "b" * 40})
        with pytest.raises(PinError, match="tag resolves to"):
            self.check(git)

    def test_unpublished_tag_is_bootstrapped(self):
        git = FakeGit()
        assert self.check(git).resolution is Resolution.BOOTSTRAPPED
        assert git.pushed == [(SHA, "modules/s3/v1.0.0")]

    @pytest.mark.parametrize(
        "git,sha,fragment",
        [
            (FakeGit(), "abc", "not a 40-character commit"),
            (FakeGit(commits=()), SHA, "not in this clone"),
            (FakeGit(on_main=()), SHA, "not on origin/main"),
            (FakeGit(files={}), SHA, "declares version None, not 1.0.0"),
            (FakeGit(files={(SHA, "modules/s3/module.yaml"): MANIFEST_V1.replace("1.0.0", "1.1.0")}), SHA, "declares version 1.1.0"),
        ],
    )
    def test_unpublished_tag_rejections(self, git, sha, fragment):
        with pytest.raises(PinError, match=fragment):
            self.check(git, sha=sha)
        assert git.pushed == []

    def test_unpublished_tag_refused_without_switch(self):
        git = FakeGit()
        verifier = ModulePinVerifier(git, Path("/tmp/m"))
        with pytest.raises(PinError, match="not published on the remote.*--allow-unpublished"):
            verifier.check("development/platform/smoke", "bucket", ModulePin("modules/s3/v1.0.0", SHA))
        assert git.pushed == []

    def test_published_tag_needs_no_switch(self):
        git = FakeGit(published={"modules/s3/v1.0.0": SHA})
        check = ModulePinVerifier(git, Path("/tmp/m")).check("s", "bucket", ModulePin("modules/s3/v1.0.0", SHA))
        assert check.resolution is Resolution.PUBLISHED

    def test_verify_all_stacks(self):
        stacks = [metadata("development", "platform", "smoke", modules={"bucket": {"ref": "modules/s3/v1.0.0", "sha": SHA}})]
        checks = ModulePinVerifier(FakeGit(), Path("/tmp/m"), allow_unpublished=True).verify(stacks)
        assert [c.render() for c in checks] == ["development/platform/smoke: module.bucket modules/s3/v1.0.0 @ aaaaaaaaaaaa (bootstrapped)"]


def _git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


@pytest.fixture
def clone(tmp_path):
    """A clone of a bare 'origin' whose main has modules/s3/module.yaml at 1.0.0."""
    origin = tmp_path / "origin.git"
    work = tmp_path / "work"
    _git(tmp_path, "init", "-q", "--bare", "-b", "main", str(origin))
    _git(tmp_path, "clone", "-q", str(origin), str(work))
    for key, value in (("user.email", "t@example.com"), ("user.name", "t"), ("commit.gpgsign", "false"), ("tag.gpgsign", "false")):
        _git(work, "config", key, value)
    (work / "modules" / "s3").mkdir(parents=True)
    (work / "modules" / "s3" / "module.yaml").write_text(MANIFEST_V1)
    _git(work, "add", ".")
    _git(work, "commit", "-q", "-m", "s3 1.0.0")
    _git(work, "push", "-q", "origin", "HEAD:main")
    _git(work, "fetch", "-q", "origin")
    return work


class TestGitCliIntegration:
    def test_unpublished_then_published(self, clone, tmp_path, make_stack):
        sha = _git(clone, "rev-parse", "HEAD")
        git = GitCli(clone)
        assert git.remote_tag_commit("modules/s3/v1.0.0") is None
        assert git.is_commit(sha) and git.is_ancestor(sha, "origin/main")
        assert manifest_version(git.show(sha, "modules/s3/module.yaml")) == "1.0.0"
        assert git.show(sha, "missing") is None

        mirror = tmp_path / "mirror.git"
        git.publish_tag(mirror, sha, "modules/s3/v1.0.0")
        assert _git(tmp_path, "--git-dir", str(mirror), "rev-parse", "refs/tags/modules/s3/v1.0.0") == sha

        _git(clone, "tag", "-a", "-m", "release", "modules/s3/v1.0.0", sha)
        _git(clone, "push", "-q", "origin", "modules/s3/v1.0.0")
        assert git.remote_tag_commit("modules/s3/v1.0.0") == sha  # annotated tag peeled to its commit

    def test_unreachable_remote_raises(self, clone):
        _git(clone, "remote", "set-url", "origin", str(clone.parent / "nowhere.git"))
        with pytest.raises(subprocess.CalledProcessError):
            GitCli(clone).remote_tag_commit("modules/s3/v1.0.0")

    def test_cli_prints_exports_for_bootstrapped_pins(self, clone, tmp_path, capsys):
        sha = _git(clone, "rev-parse", "HEAD")
        stack = clone / "environments" / "development" / "platform" / "smoke"
        stack.mkdir(parents=True)
        (stack / "iacp.stack.json").write_text(
            '{"environment": "development", "team": "platform", "stack": "smoke", "region": "us-east-1",'
            ' "lifecycle": "active", "backend": "local",'
            f' "modules": {{"bucket": {{"ref": "modules/s3/v1.0.0", "sha": "{sha}"}}}}}}'
        )
        mirror = tmp_path / "mirror.git"
        argv = ["--repo-root", str(clone), "verify-pins", str(stack), "--mirror", str(mirror), "--allow-unpublished"]
        argv += ["--github-env", ""]
        assert main(argv) == 0
        captured = capsys.readouterr()
        assert "export GIT_CONFIG_COUNT=2" in captured.out
        assert "(bootstrapped)" in captured.err

        env_file = tmp_path / "github_env"
        assert main(argv[:-1] + [str(env_file)]) == 0
        assert "GIT_CONFIG_KEY_0=url.file://" in env_file.read_text()

    def test_cli_rejects_commit_not_on_main(self, clone, tmp_path, capsys):
        _git(clone, "checkout", "-q", "-b", "feature")
        (clone / "modules" / "s3" / "module.yaml").write_text(MANIFEST_V1 + "# pr change\n")
        _git(clone, "commit", "-q", "-am", "pr change")
        sha = _git(clone, "rev-parse", "HEAD")
        stack = clone / "environments" / "development" / "platform" / "smoke"
        stack.mkdir(parents=True)
        (stack / "iacp.stack.json").write_text(
            '{"environment": "development", "team": "platform", "stack": "smoke", "region": "us-east-1",'
            ' "lifecycle": "active", "backend": "local",'
            f' "modules": {{"bucket": {{"ref": "modules/s3/v1.0.0", "sha": "{sha}"}}}}}}'
        )
        argv = ["--repo-root", str(clone), "verify-pins", str(stack), "--mirror", str(tmp_path / "m.git")]
        argv += ["--allow-unpublished", "--github-env", ""]
        assert main(argv) == 1
        assert "is not on origin/main" in capsys.readouterr().err


class TestMirrorEndToEnd:
    def test_insteadof_env_makes_clone_resolve_tag_from_mirror(self, clone, tmp_path):
        """The exported GIT_CONFIG_* variables make a clone of <repo>.git at the tag read the mirror."""
        sha = _git(clone, "rev-parse", "HEAD")
        stack = clone / "environments" / "development" / "platform" / "smoke"
        stack.mkdir(parents=True)
        (stack / "iacp.stack.json").write_text(
            '{"environment": "development", "team": "platform", "stack": "smoke", "region": "us-east-1",'
            ' "lifecycle": "active", "backend": "local",'
            f' "modules": {{"bucket": {{"ref": "modules/s3/v1.0.0", "sha": "{sha}"}}}}}}'
        )
        env_file = tmp_path / "github_env"
        repo_url = "https://github.invalid/acme/modules.git"
        argv = ["--repo-root", str(clone), "verify-pins", str(stack), "--mirror", str(tmp_path / "mirror.git")]
        argv += ["--allow-unpublished", "--repo-url", repo_url, "--github-env", str(env_file)]
        assert main(argv) == 0
        env = dict(os.environ, **dict(line.split("=", 1) for line in env_file.read_text().splitlines()))
        for i, url in enumerate((repo_url, repo_url.removesuffix(".git"))):  # Terraform and Checkov spellings
            dest = tmp_path / f"out{i}"
            subprocess.run(
                ["git", "clone", "-q", "--depth=1", "-b", "modules/s3/v1.0.0", url, str(dest)],
                check=True,
                capture_output=True,
                env=env,
            )
            assert _git(dest, "rev-parse", "HEAD") == sha
