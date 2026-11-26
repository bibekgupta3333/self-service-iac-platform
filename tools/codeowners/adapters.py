"""Filesystem adapters: the only place this tool reads or writes files."""

from __future__ import annotations

import json
from pathlib import Path

import yaml

from .models import ENVIRONMENTS, ConfigError, OwnersConfig

STACK_FILE = "iacp.stack.json"


class RepoFiles:
    """Reads teams.yaml and stack metadata, and reads or writes CODEOWNERS, under a repo root."""

    def __init__(self, root: Path) -> None:
        self._root = root

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
        """The team of every stack root (environments/<env>/<team>/<stack>/iacp.stack.json).
        The directory names the team; the metadata must agree with it."""
        teams: set[str] = set()
        for env in ENVIRONMENTS:
            for meta in sorted((self._root / "environments" / env).glob(f"*/*/{STACK_FILE}")):
                team_dir = meta.parent.parent.name
                try:
                    declared = json.loads(meta.read_text(encoding="utf-8")).get("team")
                except json.JSONDecodeError as exc:
                    raise ConfigError(f"{meta.relative_to(self._root)}: {exc}") from exc
                if declared != team_dir:
                    raise ConfigError(
                        f"{meta.relative_to(self._root)}: team {declared!r} disagrees with its directory {team_dir!r}"
                    )
                teams.add(team_dir)
        return teams

    def read(self, rel: str) -> str | None:
        try:
            return (self._root / rel).read_text(encoding="utf-8")
        except FileNotFoundError:
            return None

    def write(self, rel: str, text: str) -> None:
        (self._root / rel).write_text(text, encoding="utf-8")
