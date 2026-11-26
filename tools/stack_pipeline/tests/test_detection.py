import json
import subprocess

import pytest

from tools.stack_pipeline import (
    DetectionError,
    DetectionPolicy,
    StackDetector,
    StackMetadataError,
    StackRef,
    check_guard_rails,
    dependency_conflicts,
    stack_roots,
    triggers_full_run,
    unmapped_paths,
)
from tools.stack_pipeline.adapters import FileSystemStackRepository, GitDiff
from tools.stack_pipeline.cli import main
from tools.stack_pipeline.models import StackMetadata, is_non_stack_file

from .conftest import FakeRepository, metadata, stack_json

SMOKE = StackRef("development", "platform", "smoke")


class TestStackRef:
    @pytest.mark.parametrize(
        "path,expected",
        [
            ("environments/development/platform/smoke/main.tf", SMOKE),
            ("environments/development/platform/smoke/nested/dir/file.json", SMOKE),
            ("/environments/development/platform/smoke/main.tf", SMOKE),
            ("environments/staging/payments/orders/iacp.stack.json", StackRef("staging", "payments", "orders")),
            ("environments/README.md", None),
            ("environments/development/.gitkeep", None),
            ("environments/development/platform/smoke", None),  # a file named like a root
            ("environments/sandbox/platform/smoke/main.tf", None),  # unknown environment
            ("environments/development/Platform/smoke/main.tf", None),  # invalid team
            ("environments/development/platform/s/main.tf", None),  # name too short
            ("environments/development/platform/smoke\n/main.tf", None),  # trailing newline in a name
            ("environments/development/my team/x/main.tf", None),  # space in a name
            ("modules/s3/main.tf", None),
            ("docs/environments/development/platform/smoke/main.tf", None),
        ],
    )
    def test_from_path(self, path, expected):
        assert StackRef.from_path(path) == expected

    @pytest.mark.parametrize(
        "a,b",
        [
            (("development", "a-b", "cc"), ("development", "aa", "b-cc")),
            (("development", "ab-cd", "ef"), ("development", "ab", "cd-ef")),
            (("development", "x--y", "zz"), ("development", "x", "-y-zz")),
        ],
    )
    def test_ids_are_injective(self, a, b):
        assert StackRef(*a).id != StackRef(*b).id

    def test_id_round_trips(self):
        assert tuple(SMOKE.id.split("__")) == ("development", "platform", "smoke")

    def test_derived_names(self):
        assert SMOKE.path == "environments/development/platform/smoke"
        assert SMOKE.key == "development/platform/smoke"
        assert SMOKE.id == "development__platform__smoke"


class TestStackMetadata:
    def test_matrix_entry(self):
        entry = metadata("development", "platform", "smoke").matrix_entry()
        assert entry == {
            "id": "development__platform__smoke",
            "path": "environments/development/platform/smoke",
            "environment": "development",
            "team": "platform",
            "stack": "smoke",
            "region": "us-east-1",
            "lifecycle": "active",
            "backend": "local",
        }

    def test_modules_and_depends_on_parsed(self):
        meta = metadata("development", "platform", "smoke", depends_on=["development/platform/base"])
        assert meta.depends_on == ("development/platform/base",)
        assert meta.modules["bucket"].ref == "modules/s3/v1.0.0"

    @pytest.mark.parametrize(
        "overrides,fragment",
        [
            ({"team": "payments"}, "does not match the directory"),
            ({"environment": "staging"}, "does not match the directory"),
            ({"region": ""}, "missing region"),
            ({"lifecycle": None, "backend": 3}, "missing lifecycle, backend"),
        ],
    )
    def test_rejects_bad_metadata(self, overrides, fragment):
        with pytest.raises(StackMetadataError, match=fragment):
            StackMetadata.from_json(SMOKE, stack_json("development", "platform", "smoke", **overrides))

    def test_rejects_non_object(self):
        with pytest.raises(StackMetadataError, match="JSON object"):
            StackMetadata.from_json(SMOKE, ["not", "an", "object"])


class TestPureFunctions:
    def test_stack_roots_dedupes_and_sorts(self):
        paths = [
            "environments/staging/payments/orders/main.tf",
            "environments/development/platform/smoke/main.tf",
            "environments/development/platform/smoke/iacp.stack.json",
            "README.md",
        ]
        assert stack_roots(paths) == [SMOKE, StackRef("staging", "payments", "orders")]

    @pytest.mark.parametrize(
        "paths,prefixes,expected",
        [
            ([".github/workflows/pr-validate.yml"], (".github/workflows/pr-validate.yml",), True),
            (["tools/stack_pipeline/cli.py"], ("tools/stack_pipeline/",), True),
            (["tools/manifest_validator/cli.py"], ("tools/stack_pipeline/",), False),
            (["anything"], ("",), False),  # an empty prefix never matches everything
            (["anything"], (), False),
        ],
    )
    def test_triggers_full_run(self, paths, prefixes, expected):
        assert triggers_full_run(paths, prefixes) is expected

    @pytest.mark.parametrize(
        "dep",
        ["development/platform/base", "development__platform__base", "environments/development/platform/base"],
    )
    def test_dependency_conflicts_by_any_alias(self, dep):
        stacks = [metadata("development", "platform", "base"), metadata("development", "platform", "app", depends_on=[dep])]
        assert dependency_conflicts(stacks) == [("development/platform/app", "development/platform/base")]

    def test_dependency_outside_change_set_is_fine(self):
        stacks = [metadata("development", "platform", "app", depends_on=["development/platform/base"])]
        assert dependency_conflicts(stacks) == []

    def test_self_dependency_is_ignored(self):
        stacks = [metadata("development", "platform", "app", depends_on=["development/platform/app"])]
        assert dependency_conflicts(stacks) == []

    def test_guard_rail_limit(self):
        stacks = [metadata("development", "platform", f"s{i:02d}") for i in range(3)]
        check_guard_rails(stacks, 3)
        with pytest.raises(DetectionError, match="3 stacks changed \\(limit 2\\)"):
            check_guard_rails(stacks, 2)

    def test_guard_rail_dependency(self):
        stacks = [
            metadata("development", "platform", "base"),
            metadata("development", "platform", "app", depends_on=["development/platform/base"]),
        ]
        with pytest.raises(DetectionError, match="development/platform/app -> development/platform/base"):
            check_guard_rails(stacks, 20)


# Every path here is under environments/ but outside a valid stack root.
UNMAPPED = [
    "environments/dev/platform/x/main.tf",  # unknown environment
    "environments/development/Platform/x/main.tf",  # uppercase segment
    "environments/development/platform/evil.tf",  # file directly under a team
    "environments/development/my team/x/main.tf",  # space in a segment
    "environments/development/platform/x Y/main.tf",  # space in the stack segment
    "environments/rogue.tf",  # file directly under environments/
    "environments/development/notes.md",  # file directly under an environment
    "environments/staging/README.md",  # only the top-level README is allow-listed
]


class TestFailClosed:
    @pytest.mark.parametrize("path", UNMAPPED)
    def test_unmapped_path_fails(self, path):
        assert unmapped_paths([path]) == [path]
        with pytest.raises(DetectionError, match="not inside an environments/<env>/<team>/<stack>/ root"):
            StackDetector(FakeRepository()).detect([path, "README.md"])

    @pytest.mark.parametrize(
        "path",
        ["environments/README.md", "environments/development/.gitkeep", "environments/production/.gitkeep", "README.md"],
    )
    def test_allow_listed_and_outside_paths_pass(self, path):
        assert unmapped_paths([path]) == []
        assert StackDetector(FakeRepository()).detect([path]).count == 0

    def test_is_non_stack_file_is_exact(self):
        assert is_non_stack_file("environments/README.md")
        assert not is_non_stack_file("environments/README.md.tf")
        assert not is_non_stack_file("environments/sandbox/.gitkeep")

    def test_all_unmapped_paths_are_reported_together(self):
        with pytest.raises(DetectionError) as exc:
            StackDetector(FakeRepository()).detect(UNMAPPED[:2])
        assert UNMAPPED[0] in str(exc.value) and UNMAPPED[1] in str(exc.value)

    def test_root_with_files_but_no_stack_json_fails(self):
        rogue = StackRef("development", "platform", "rogue")
        with pytest.raises(DetectionError, match="have files but no iacp.stack.json: environments/development/platform/rogue"):
            StackDetector(FakeRepository(orphans=(rogue,))).detect(["environments/development/platform/rogue/main.tf"])

    def test_root_whose_directory_is_gone_is_skipped(self):
        result = StackDetector(FakeRepository()).detect(["environments/development/platform/gone/main.tf"])
        assert result.count == 0 and [r.path for r in result.skipped] == ["environments/development/platform/gone"]

    def test_cli_exits_non_zero_on_reviewer_cases(self, tmp_path, make_stack, capsys):
        rogue = tmp_path / "environments" / "development" / "platform" / "rogue"
        rogue.mkdir(parents=True)
        (rogue / "main.tf").write_text("resource {}\n")
        changed = tmp_path / "changed.txt"
        changed.write_text("environments/development/platform/rogue/main.tf\n")
        rc = main(["--repo-root", str(tmp_path), "detect", "--files-from", str(changed), "--github-output", ""])
        assert rc == 1 and "no iacp.stack.json" in capsys.readouterr().err
        changed.write_text("\n".join(UNMAPPED) + "\n")
        rc = main(["--repo-root", str(tmp_path), "detect", "--files-from", str(changed), "--github-output", ""])
        assert rc == 1 and "not inside" in capsys.readouterr().err


class TestStackDetector:
    def test_maps_changes_to_existing_stacks(self):
        repo = FakeRepository(metadata("development", "platform", "smoke"), metadata("staging", "payments", "orders"))
        result = StackDetector(repo).detect(["environments/development/platform/smoke/main.tf", "README.md"])
        assert [s.ref for s in result.stacks] == [SMOKE]
        assert result.count == 1 and not result.full_run and result.skipped == ()

    def test_deleted_root_is_skipped(self):
        result = StackDetector(FakeRepository()).detect(["environments/development/platform/gone/main.tf"])
        assert result.count == 0
        assert result.to_dict()["skipped"] == ["environments/development/platform/gone"]

    def test_no_stack_changes(self):
        result = StackDetector(FakeRepository(metadata("development", "platform", "smoke"))).detect(["docs/x.md"])
        assert result.to_dict() == {"count": 0, "full_run": False, "stacks": [], "skipped": []}

    def test_full_run_includes_every_stack(self):
        repo = FakeRepository(metadata("development", "platform", "smoke"), metadata("staging", "payments", "orders"))
        policy = DetectionPolicy(full_run_prefixes=(".github/workflows/pr-validate.yml",))
        result = StackDetector(repo, policy).detect([".github/workflows/pr-validate.yml"])
        assert result.full_run and result.count == 2

    def test_full_run_is_not_capped_by_change_limit(self):
        repo = FakeRepository(*(metadata("development", "platform", f"s{i:02d}") for i in range(3)))
        policy = DetectionPolicy(max_stacks=1, full_run_prefixes=("tools/stack_pipeline/",))
        assert StackDetector(repo, policy).detect(["tools/stack_pipeline/cli.py"]).count == 3

    def test_limit_applies_to_change_sets(self):
        repo = FakeRepository(*(metadata("development", "platform", f"s{i:02d}") for i in range(3)))
        paths = [f"environments/development/platform/s{i:02d}/main.tf" for i in range(3)]
        with pytest.raises(DetectionError):
            StackDetector(repo, DetectionPolicy(max_stacks=2)).detect(paths)


class TestFileSystemStackRepository:
    def test_load_and_all_refs(self, tmp_path, make_stack):
        make_stack("development", "platform", "smoke")
        make_stack("staging", "payments", "orders")
        (tmp_path / "environments" / "development" / "platform" / "empty").mkdir()
        repo = FileSystemStackRepository(tmp_path)
        assert repo.all_refs() == [SMOKE, StackRef("staging", "payments", "orders")]
        assert repo.load(SMOKE).region == "us-east-1"
        assert repo.load(StackRef("development", "platform", "empty")) is None

    def test_has_files(self, tmp_path, make_stack):
        make_stack("development", "platform", "smoke")
        empty = tmp_path / "environments" / "development" / "platform" / "empty" / "sub"
        empty.mkdir(parents=True)
        repo = FileSystemStackRepository(tmp_path)
        assert repo.has_files(SMOKE)
        assert not repo.has_files(StackRef("development", "platform", "empty"))  # only empty dirs
        assert not repo.has_files(StackRef("development", "platform", "missing"))

    def test_invalid_json(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke")
        (root / "iacp.stack.json").write_text("{not json")
        with pytest.raises(StackMetadataError, match="invalid JSON"):
            FileSystemStackRepository(tmp_path).load(SMOKE)

    def test_load_dir(self, tmp_path, make_stack):
        root = make_stack("development", "platform", "smoke")
        repo = FileSystemStackRepository(tmp_path)
        assert repo.load_dir(root).ref == SMOKE
        with pytest.raises(StackMetadataError, match="not a stack root"):
            repo.load_dir(root / "..")
        with pytest.raises(StackMetadataError, match="not inside"):
            repo.load_dir(tmp_path.parent)
        (root / "iacp.stack.json").unlink()
        with pytest.raises(StackMetadataError, match="not found"):
            repo.load_dir(root)


def _git(cwd, *args):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True)


class TestGitDiff:
    def test_changed_files_reports_renames_as_delete_and_add(self, tmp_path, make_stack):
        _git(tmp_path, "init", "-q", "-b", "main")
        _git(tmp_path, "config", "user.email", "test@example.com")
        _git(tmp_path, "config", "user.name", "test")
        make_stack("development", "platform", "old")
        _git(tmp_path, "add", ".")
        _git(tmp_path, "commit", "-q", "-m", "base")
        _git(tmp_path, "mv", "environments/development/platform/old", "environments/development/platform/new")
        _git(tmp_path, "commit", "-q", "-m", "move")
        files = GitDiff(tmp_path).changed_files("HEAD~1", "HEAD")
        assert "environments/development/platform/old/main.tf" in files
        assert "environments/development/platform/new/main.tf" in files


class TestCli:
    def test_detect_writes_github_output(self, tmp_path, make_stack, capsys):
        make_stack("development", "platform", "smoke")
        changed = tmp_path / "changed.txt"
        changed.write_text("environments/development/platform/smoke/main.tf\nREADME.md\n")
        out = tmp_path / "gh_output"
        rc = main(["--repo-root", str(tmp_path), "detect", "--files-from", str(changed), "--github-output", str(out)])
        assert rc == 0
        printed = json.loads(capsys.readouterr().out)
        assert printed["count"] == 1
        lines = dict(line.split("=", 1) for line in out.read_text().splitlines())
        assert json.loads(lines["matrix"])[0]["id"] == "development__platform__smoke"
        assert lines["count"] == "1" and lines["full_run"] == "false"

    def test_detect_from_stdin(self, tmp_path, make_stack, capsys, monkeypatch):
        make_stack("development", "platform", "smoke")
        monkeypatch.setattr("sys.stdin", __import__("io").StringIO("environments/development/platform/smoke/x.tf\n"))
        assert main(["--repo-root", str(tmp_path), "detect", "--files-from", "-", "--github-output", ""]) == 0
        assert json.loads(capsys.readouterr().out)["count"] == 1

    def test_detect_guard_rail_exit_code(self, tmp_path, make_stack, capsys):
        for name in ("aa", "bb"):
            make_stack("development", "platform", name)
        changed = tmp_path / "changed.txt"
        changed.write_text("environments/development/platform/aa/x\nenvironments/development/platform/bb/x\n")
        rc = main(["--repo-root", str(tmp_path), "detect", "--files-from", str(changed), "--max-stacks", "1"])
        assert rc == 1
        assert "limit 1" in capsys.readouterr().err

    def test_detect_bad_git_ref_exit_code(self, tmp_path, capsys):
        _git(tmp_path, "init", "-q")
        rc = main(["--repo-root", str(tmp_path), "detect", "--base", "nope", "--head", "nope2", "--github-output", ""])
        assert rc == 2
