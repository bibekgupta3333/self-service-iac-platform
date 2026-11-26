"""I/O adapters: git history and the working tree. No decisions are made here."""

from __future__ import annotations

import subprocess
from pathlib import Path
from typing import Any, Protocol, Sequence

import yaml

MANIFEST_NAME = "module.yaml"
CHANGELOG_NAME = "CHANGELOG.md"


class GitError(RuntimeError):
    """A git command failed."""


class GitHistory(Protocol):
    """What the gate needs from git (lets tests substitute a fake)."""

    def tags(self, pattern: str) -> list[str]: ...

    def files_at(self, ref: str, directory: str) -> dict[str, str]: ...

    def changed_files(self, ref: str, directory: str) -> list[str]: ...


class GitRepository:
    """Runs read-only git commands in a repository."""

    def __init__(self, root: Path, git: str = "git") -> None:
        self.root = Path(root)
        self._git = git

    def _run(self, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
        proc = subprocess.run(
            [self._git, "-C", str(self.root), *args],
            capture_output=True, text=True, check=False,
        )
        if check and proc.returncode != 0:
            raise GitError(f"git {' '.join(args)} failed: {proc.stderr.strip()}")
        return proc

    def tags(self, pattern: str) -> list[str]:
        """Tag names matching a glob, for example ``modules/s3/v*``."""
        return [t for t in self._run("tag", "--list", pattern).stdout.splitlines() if t]

    def has_ref(self, ref: str) -> bool:
        return self._run("rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}", check=False).returncode == 0

    def files_at(self, ref: str, directory: str) -> dict[str, str]:
        """Contents of the top-level files of ``directory`` at ``ref`` (name -> text)."""
        listing = self._run("ls-tree", "--name-only", f"{ref}:{directory}", check=False)
        if listing.returncode != 0:
            return {}
        names = [n for n in listing.stdout.splitlines() if n.endswith((".tf", ".yaml", ".md"))]
        return {name: self._run("show", f"{ref}:{directory}/{name}").stdout for name in names}

    def changed_files(self, ref: str, directory: str) -> list[str]:
        """Paths under ``directory`` (relative to it) that differ between ``ref`` and the working tree.

        Untracked files are included so a local run sees new files before ``git add``.
        """
        prefix = directory.rstrip("/") + "/"
        tracked = self._run("diff", "--name-only", ref, "--", directory).stdout.splitlines()
        untracked = self._run("ls-files", "--others", "--exclude-standard", "--", directory).stdout.splitlines()
        return sorted({p[len(prefix):] for p in tracked + untracked if p.startswith(prefix)})

    def changed_paths(self, base: str, head: str = "HEAD") -> list[str]:
        """Repository paths changed between two commits."""
        return [p for p in self._run("diff", "--name-only", base, head).stdout.splitlines() if p]


class ModuleWorkspace:
    """Reads modules/<name>/ from the working tree."""

    def __init__(self, repo_root: Path, modules_dir: str = "modules") -> None:
        self.repo_root = Path(repo_root)
        self.modules_dir = modules_dir

    def relative_dir(self, module: str) -> str:
        return f"{self.modules_dir}/{module}"

    def path(self, module: str) -> Path:
        return self.repo_root / self.modules_dir / module

    def module_names(self) -> list[str]:
        """Every module directory that has a manifest (``_shared`` and hidden dirs are skipped)."""
        root = self.repo_root / self.modules_dir
        if not root.is_dir():
            raise FileNotFoundError(f"modules directory not found: {root}")
        return sorted(
            p.name for p in root.iterdir()
            if p.is_dir() and not p.name.startswith(("_", ".")) and (p / MANIFEST_NAME).is_file()
        )

    def terraform_files(self, module: str) -> dict[str, str]:
        return {p.name: p.read_text(encoding="utf-8") for p in sorted(self.path(module).glob("*.tf"))}

    def manifest(self, module: str) -> dict[str, Any]:
        path = self.path(module) / MANIFEST_NAME
        if not path.is_file():
            raise FileNotFoundError(f"{path} not found")
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
        if not isinstance(data, dict):
            raise ValueError(f"{path} must be a mapping")
        return data

    def changelog(self, module: str) -> str | None:
        path = self.path(module) / CHANGELOG_NAME
        return path.read_text(encoding="utf-8") if path.is_file() else None


def modules_in_paths(paths: Sequence[str], modules_dir: str = "modules") -> list[str]:
    """Module names touched by a list of repository paths. Pure."""
    prefix = modules_dir.rstrip("/") + "/"
    names = {p[len(prefix):].split("/", 1)[0] for p in paths if p.startswith(prefix) and p.count("/") >= 2}
    return sorted(n for n in names if not n.startswith(("_", ".")))
