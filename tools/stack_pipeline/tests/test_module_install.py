import json
import subprocess
from pathlib import Path

import pytest

from tools.stack_pipeline.adapters import GitCli
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.module_install import (
    InstalledModule,
    ModuleInstallVerifier,
    expected_source,
    is_local_source,
    manifest_problems,
    parse_manifest,
)
from tools.stack_pipeline.models import ModulePin
from tools.stack_pipeline.module_pins import PinError

from .conftest import metadata

REPO = "https://github.com/o/r.git"
SHA = "a" * 40
SRC = f"git::{REPO}//modules/s3?ref=modules/s3/v1.0.0"


def meta(**modules):
    pins = modules or {"bucket": {"ref": "modules/s3/v1.0.0", "sha": SHA}}
    return metadata("development", "platform", "smoke", modules=pins)


def mod(key, source=SRC):
    return InstalledModule(key, source)


class TestPure:
    def test_parse_manifest_drops_root(self):
        data = {"Modules": [{"Key": "", "Source": "", "Dir": "."}, {"Key": "bucket", "Source": SRC, "Dir": "x"}]}
        assert parse_manifest(data) == (mod("bucket"),)

    @pytest.mark.parametrize("data", [{}, {"Modules": "x"}, []])
    def test_parse_manifest_rejects(self, data):
        with pytest.raises(PinError):
            parse_manifest(data)

    def test_expected_source_matches_generator_form(self):
        assert expected_source(REPO, ModulePin("modules/s3/v1.0.0", SHA)) == SRC

    @pytest.mark.parametrize("src,local", [("./x", True), ("../x", True), (SRC, False), ("/abs", False), ("x", False)])
    def test_is_local_source(self, src, local):
        assert is_local_source(src) is local

    def test_exact_match(self):
        assert manifest_problems(meta(), [mod("bucket"), mod("bucket.inner", "./inner")], REPO) == []

    @pytest.mark.parametrize(
        "installed,fragment",
        [
            ([mod("bucket"), mod("extra")], "module.extra: installed from"),  # call without a pin
            ([], "module.bucket: pinned in iacp.stack.json but not a module call"),  # pin without a call
            ([mod("bucket", SRC.replace("v1.0.0", "v1.1.0"))], "does not match its pin"),  # other tag
            ([mod("bucket", "../../../../modules/s3")], "does not match its pin"),  # local source
            ([mod("bucket", SRC.replace("o/r", "evil/r"))], "does not match its pin"),  # other repo
            ([mod("bucket", SRC.replace("?ref=modules/s3/v1.0.0", "?ref=main"))], "does not match its pin"),
            ([mod("bucket"), mod("bucket.inner", "git::https://x/y.git")], "nested module call uses non-local source"),
        ],
    )
    def test_mismatches(self, installed, fragment):
        problems = manifest_problems(meta(), installed, REPO)
        assert any(fragment in p for p in problems), problems

    def test_bad_pin_ref_is_a_problem(self):
        problems = manifest_problems(meta(bucket={"ref": "main", "sha": SHA}), [mod("bucket")], REPO)
        assert problems and "not a module release tag" in problems[0]


class FakeHead:
    def __init__(self, head):
        self.head = head

    def head_commit(self, git_dir):
        return self.head


class TestVerifier:
    def _stack(self, tmp_path, with_git=True):
        if with_git:
            (tmp_path / ".terraform" / "modules" / "bucket" / ".git").mkdir(parents=True)
        return tmp_path

    def test_ok(self, tmp_path):
        lines = ModuleInstallVerifier(FakeHead(SHA), REPO).verify(self._stack(tmp_path), meta(), [mod("bucket")])
        assert lines == ["development/platform/smoke: module.bucket modules/s3/v1.0.0 installed at aaaaaaaaaaaa"]

    def test_wrong_commit(self, tmp_path):
        with pytest.raises(PinError, match=f"fetched commit {'b' * 40} is not the recorded {SHA}"):
            ModuleInstallVerifier(FakeHead("b" * 40), REPO).verify(self._stack(tmp_path), meta(), [mod("bucket")])

    def test_missing_checkout_git_dir(self, tmp_path):
        with pytest.raises(PinError, match="fetched commit cannot be checked"):
            ModuleInstallVerifier(FakeHead(SHA), REPO).verify(self._stack(tmp_path, False), meta(), [mod("bucket")])

    def test_manifest_problems_reported_before_commits(self, tmp_path):
        with pytest.raises(PinError, match="module.extra"):
            ModuleInstallVerifier(FakeHead(SHA), REPO).verify(self._stack(tmp_path), meta(), [mod("bucket"), mod("extra")])


def _git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def _repo(path: Path) -> str:
    path.mkdir(parents=True)
    _git(path, "init", "-q")
    for key, value in (("user.email", "t@example.com"), ("user.name", "t"), ("commit.gpgsign", "false")):
        _git(path, "config", key, value)
    (path / "f").write_text("x")
    _git(path, "add", ".")
    _git(path, "commit", "-q", "-m", "c")
    return _git(path, "rev-parse", "HEAD")


class TestGitCliHead:
    def test_head_commit_and_no_walk_up(self, tmp_path):
        sha = _repo(tmp_path / "repo")
        git = GitCli(tmp_path)
        assert git.head_commit(tmp_path / "repo" / ".git") == sha
        (tmp_path / "repo" / "sub").mkdir()
        # A directory without its own .git must not resolve to the enclosing repository.
        assert git.head_commit(tmp_path / "repo" / "sub" / ".git") is None


class TestCli:
    def _stack(self, tmp_path, make_stack, sha, source):
        root = make_stack("development", "platform", "smoke", modules={"bucket": {"ref": "modules/s3/v1.0.0", "sha": sha}})
        (root / ".terraform" / "modules").mkdir(parents=True)
        manifest = {"Modules": [{"Key": "", "Source": "", "Dir": "."}, {"Key": "bucket", "Source": source, "Dir": "x"}]}
        (root / ".terraform" / "modules" / "modules.json").write_text(json.dumps(manifest))
        return root

    def test_verify_modules_ok(self, tmp_path, make_stack, capsys):
        sha = _repo(tmp_path / "checkout")
        root = self._stack(tmp_path, make_stack, sha, SRC)
        (tmp_path / "checkout").rename(root / ".terraform" / "modules" / "bucket")
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root), "--repo-url", REPO]) == 0
        assert "installed at" in capsys.readouterr().out

    def test_verify_modules_with_relative_stack_dir(self, tmp_path, make_stack, capsys, monkeypatch):
        """As in CI: cwd is the stack root, the stack is '.', and --repo-root is elsewhere."""
        sha = _repo(tmp_path / "checkout")
        root = self._stack(tmp_path, make_stack, sha, SRC)
        (tmp_path / "checkout").rename(root / ".terraform" / "modules" / "bucket")
        monkeypatch.chdir(root)
        assert main(["--repo-root", str(tmp_path), "verify-modules", ".", "--repo-url", REPO]) == 0
        assert "installed at" in capsys.readouterr().out

    def test_verify_modules_wrong_source(self, tmp_path, make_stack, capsys):
        root = self._stack(tmp_path, make_stack, SHA, SRC.replace("v1.0.0", "v9.9.9"))
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root), "--repo-url", REPO]) == 1
        assert "does not match its pin" in capsys.readouterr().err

    def test_verify_modules_destroy_root_without_a_manifest(self, tmp_path, make_stack, capsys):
        """terraform init writes no modules.json for a root with no module calls (a destroy root)."""
        root = make_stack("development", "platform", "smoke", modules={}, lifecycle="destroy")
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root)]) == 0
        assert "nothing to verify" in capsys.readouterr().out

    def test_verify_modules_empty_manifest_for_a_destroy_root(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke", modules={}, lifecycle="destroy")
        (root / ".terraform" / "modules").mkdir(parents=True)
        (root / ".terraform" / "modules" / "modules.json").write_text(json.dumps({"Modules": [{"Key": "", "Source": "", "Dir": "."}]}))
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root)]) == 0

    def test_verify_modules_destroy_root_with_a_stray_module_call_still_fails(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke", modules={}, lifecycle="destroy")
        (root / ".terraform" / "modules").mkdir(parents=True)
        manifest = {"Modules": [{"Key": "", "Source": "", "Dir": "."}, {"Key": "bucket", "Source": SRC, "Dir": "x"}]}
        (root / ".terraform" / "modules" / "modules.json").write_text(json.dumps(manifest))
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root)]) == 1
        assert "has no pin" in capsys.readouterr().err

    def test_verify_modules_without_init(self, tmp_path, make_stack, capsys):
        root = make_stack("development", "platform", "smoke")
        assert main(["--repo-root", str(tmp_path), "verify-modules", str(root)]) == 1
        assert "run terraform init first" in capsys.readouterr().err
