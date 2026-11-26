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
