"""Filesystem and git adapters: the only place this tool reads or writes files."""

from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path
from typing import Callable, Iterable

import yaml

from .models import ENVIRONMENTS, ConfigError, OwnersConfig

STACK_FILE = "iacp.stack.json"

_STACK_FILE_RE = re.compile(rf"environments/({'|'.join(ENVIRONMENTS)})/([^/]+)/[^/]+/{re.escape(STACK_FILE)}")

# Lists the repo-relative paths git tracks under a directory.
TrackedFiles = Callable[[Path, str], list[str]]


def git_tracked_files(root: Path, directory: str) -> list[str]:
    """``git ls-files`` under directory: untracked or ignored leftovers never count,
    the same rule as tools.stack_pipeline's deleted-root detection."""
    try:
        out = subprocess.run(
            ["git", "ls-files", "-z", "--", f"{directory}/"],
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        raise ConfigError(f"git ls-files {directory}/ failed: {exc}") from exc
    return [p for p in out.split("\0") if p]


def stack_files(paths: Iterable[str]) -> list[tuple[str, str]]:
    """[(path, team directory)] for every environments/<env>/<team>/<stack>/iacp.stack.json
    among paths, sorted. Pure."""
    found = []
    for p in paths:
        m = _STACK_FILE_RE.fullmatch(p)
        if m:
            found.append((p, m.group(2)))
    return sorted(found)


class RepoFiles:
    """Reads teams.yaml and tracked stack metadata, and reads or writes CODEOWNERS, under a repo root."""

    def __init__(self, root: Path, tracked: TrackedFiles = git_tracked_files) -> None:
        self._root = root
        self._tracked = tracked

    def load_config(self, rel: str) -> OwnersConfig:
        path = self._root / rel
        try:
            doc = yaml.safe_load(path.read_text(encoding="utf-8"))
        except FileNotFoundError as exc:
            raise ConfigError(f"{rel} not found") from exc
        except yaml.YAMLError as exc:
            raise ConfigError(f"{rel}: {exc}") from exc
        return OwnersConfig.from_yaml(doc)

    def stack_teams(self) -> set[str]:
        """The team of every tracked stack root. The directory names the team; the
        metadata must agree with it."""
        teams: set[str] = set()
        for rel, team_dir in stack_files(self._tracked(self._root, "environments")):
            try:
                declared = json.loads((self._root / rel).read_text(encoding="utf-8")).get("team")
            except FileNotFoundError:
                continue  # tracked but deleted in the working tree: the root is going away
            except json.JSONDecodeError as exc:
                raise ConfigError(f"{rel}: {exc}") from exc
            if declared != team_dir:
                raise ConfigError(f"{rel}: team {declared!r} disagrees with its directory {team_dir!r}")
            teams.add(team_dir)
        return teams

    def read(self, rel: str) -> str | None:
        try:
            return (self._root / rel).read_text(encoding="utf-8")
        except FileNotFoundError:
            return None

    def write(self, rel: str, text: str) -> None:
        (self._root / rel).write_text(text, encoding="utf-8")
