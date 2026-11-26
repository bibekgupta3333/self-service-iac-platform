from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest
import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]

VARIABLES_TF = """
variable "name" {
  description = "Name"
  type        = string
}

variable "size" {
  description = "Size"
  type        = number
  default     = 1
}
"""

OUTPUTS_TF = """
output "id" {
  description = "Identifier"
  value       = var.name
}
"""

CHANGELOG_MD = """# Changelog

## [1.0.0] - 2026-11-25

### Added
- Initial release.
"""


def manifest(version: str = "1.0.0", name: str = "demo") -> dict:
    return {
        "apiVersion": "iacp.dev/v1",
        "kind": "Module",
        "metadata": {
            "name": name,
            "version": version,
            "displayName": "Demo",
            "description": "Demo module.",
            "category": "storage",
            "owners": ["@acme/platform"],
            "maturity": "beta",
        },
        "compatibility": {
            "terraform": ">= 1.10, < 2.0",
            "providers": {"hashicorp/aws": ">= 5.60, < 6.0"},
            "environments": ["development"],
        },
        "inputs": {
            "type": "object",
            "required": ["name"],
            "additionalProperties": False,
            "properties": {"name": {"type": "string"}, "size": {"type": "integer", "default": 1}},
        },
        "outputs": [{"name": "id", "description": "Identifier", "wireable": True}],
        "cost": {"baseline_monthly_usd": 1.0, "drivers": ["requests"]},
    }


class Repo:
    """A throwaway git repository with a modules/ tree."""

    def __init__(self, root: Path) -> None:
        self.root = root
        root.mkdir(parents=True)
        self.git("init", "-q", "-b", "main")

    def git(self, *args: str) -> str:
        env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}
        return subprocess.run(
            ["git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-C", str(self.root), *args],
            check=True, capture_output=True, text=True, env=env,
        ).stdout.strip()

    def write(self, relative: str, text: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def add_module(self, name: str = "demo", version: str = "1.0.0", variables: str = VARIABLES_TF,
                   outputs: str = OUTPUTS_TF, changelog: str | None = CHANGELOG_MD) -> None:
        self.write(f"modules/{name}/module.yaml", yaml.safe_dump(manifest(version, name), sort_keys=False))
        self.write(f"modules/{name}/variables.tf", variables)
        self.write(f"modules/{name}/outputs.tf", outputs)
        if changelog is not None:
            self.write(f"modules/{name}/CHANGELOG.md", changelog)

    def set_version(self, name: str, version: str) -> None:
        self.write(f"modules/{name}/module.yaml", yaml.safe_dump(manifest(version, name), sort_keys=False))

    def commit(self, message: str = "change") -> str:
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)
        return self.git("rev-parse", "HEAD")

    def tag(self, name: str) -> None:
        self.git("tag", name)


@pytest.fixture
def repo(tmp_path: Path) -> Repo:
    r = Repo(tmp_path / "repo")
    r.write("modules/_shared/module.schema.json", "{}")
    return r
