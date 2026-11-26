"""I/O adapters: git and the working tree. Everything else in the package is pure."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

from .models import ENVIRONMENTS_DIR, STACK_FILE, StackMetadata, StackMetadataError, StackRef


class GitDiff:
    """Lists files changed between two commits."""

    def __init__(self, repo_root: Path) -> None:
        self.repo_root = Path(repo_root)

    def changed_files(self, base: str, head: str) -> list[str]:
        """Paths changed in ``base...head`` (merge base to head).

        ``--no-renames`` reports a moved stack as a delete plus an add, so both the
        old root (skipped: no iacp.stack.json) and the new root are seen.
        """
        out = subprocess.run(
            ["git", "diff", "--name-only", "--no-renames", "-z", f"{base}...{head}"],
            cwd=self.repo_root,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
        return [p for p in out.split("\0") if p]


class GitCli:
    """The git operations ModulePinVerifier needs, run in a local clone."""

    def __init__(self, repo_root: Path, remote: str = "origin") -> None:
        self.repo_root = Path(repo_root)
        self.remote = remote

    def _git(self, *args: str, check: bool = True, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
        return subprocess.run(["git", *args], cwd=cwd or self.repo_root, check=check, capture_output=True, text=True)

    def remote_tag_commit(self, tag: str) -> str | None:
        """Commit the remote tag points at, or None when the remote has no such tag.

        A failing ls-remote (network, auth) raises instead of returning None, so
        an unreachable remote is never mistaken for an unpublished tag.
        """
        out = self._git("ls-remote", "--tags", self.remote, f"refs/tags/{tag}", f"refs/tags/{tag}^{{}}").stdout
        refs = {ref: sha for sha, ref in (line.split("\t", 1) for line in out.splitlines() if "\t" in line)}
        return refs.get(f"refs/tags/{tag}^{{}}") or refs.get(f"refs/tags/{tag}")

    def is_commit(self, sha: str) -> bool:
        return self._git("cat-file", "-e", f"{sha}^{{commit}}", check=False).returncode == 0

    def is_ancestor(self, sha: str, ref: str) -> bool:
        return self._git("merge-base", "--is-ancestor", sha, ref, check=False).returncode == 0

    def show(self, sha: str, path: str) -> str | None:
        result = self._git("show", f"{sha}:{path}", check=False)
        return result.stdout if result.returncode == 0 else None

    def head_commit(self, git_dir: Path) -> str | None:
        """HEAD of the repository at git_dir (an explicit --git-dir, so git never walks up)."""
        result = self._git("--git-dir", str(git_dir), "rev-parse", "--verify", "HEAD^{commit}", check=False)
        return result.stdout.strip() if result.returncode == 0 else None

    def publish_tag(self, mirror: Path, sha: str, tag: str) -> None:
        """Push one commit as a tag into a bare mirror (created on first use)."""
        mirror = Path(mirror)
        if not (mirror / "HEAD").is_file():
            mirror.mkdir(parents=True, exist_ok=True)
            self._git("init", "--quiet", "--bare", str(mirror))
        self._git("push", "--quiet", "--force", str(mirror), f"{sha}:refs/tags/{tag}")


class FileSystemStackRepository:
    """Reads iacp.stack.json files from a checkout."""

    def __init__(self, repo_root: Path) -> None:
        self.repo_root = Path(repo_root)

    def load(self, ref: StackRef) -> StackMetadata | None:
        path = self.repo_root / ref.path / STACK_FILE
        if not path.is_file():
            return None
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            raise StackMetadataError(f"{ref.path}/{STACK_FILE}: invalid JSON: {exc}") from exc
        return StackMetadata.from_json(ref, data)

    def has_files(self, ref: StackRef) -> bool:
        root = self.repo_root / ref.path
        return root.is_dir() and any(p.is_file() for p in root.rglob("*"))

    def load_dir(self, stack_dir: Path) -> StackMetadata:
        """Metadata for a stack directory given on the command line."""
        resolved = Path(stack_dir).resolve()
        try:
            relative = resolved.relative_to(self.repo_root.resolve()).as_posix()
        except ValueError as exc:
            raise StackMetadataError(f"{stack_dir} is not inside {self.repo_root}") from exc
        ref = StackRef.from_path(f"{relative}/{STACK_FILE}")
        if ref is None or ref.path != relative:
            raise StackMetadataError(f"{relative} is not a stack root ({ENVIRONMENTS_DIR}/<env>/<team>/<stack>)")
        meta = self.load(ref)
        if meta is None:
            raise StackMetadataError(f"{relative}/{STACK_FILE} not found")
        return meta

    def all_refs(self) -> list[StackRef]:
        root = self.repo_root / ENVIRONMENTS_DIR
        found = (StackRef.from_path(p.relative_to(self.repo_root).as_posix()) for p in root.glob(f"*/*/*/{STACK_FILE}"))
        return sorted(ref for ref in found if ref is not None)
