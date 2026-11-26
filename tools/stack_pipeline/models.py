"""Value types for stack roots under ``environments/<env>/<team>/<stack>/``."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any, Mapping

ENVIRONMENTS_DIR = "environments"
STACK_FILE = "iacp.stack.json"
ENVIRONMENTS = ("development", "staging", "production")

# Same identity rule as backend/internal/generator (nameRe).
_NAME_RE = re.compile(r"^[a-z][a-z0-9-]{1,40}$")


class StackMetadataError(ValueError):
    """iacp.stack.json is missing a field or disagrees with its directory."""


@dataclass(frozen=True, order=True)
class StackRef:
    """Identity of one stack root, derived from its path."""

    environment: str
    team: str
    stack: str

    @classmethod
    def from_path(cls, path: str) -> StackRef | None:
        """Map any repo-relative path inside a stack root to that root, else None.

        ``environments/development/platform/smoke/main.tf`` maps to the
        ``development/platform/smoke`` stack. Paths with fewer than four segments
        (``environments/README.md``, ``environments/development/.gitkeep``) or with
        segments that are not valid names do not belong to a stack.
        """
        parts = path.replace("\\", "/").strip("/").split("/")
        if len(parts) < 5 or parts[0] != ENVIRONMENTS_DIR:
            return None
        env, team, stack = parts[1:4]
        if env not in ENVIRONMENTS or not _NAME_RE.match(team) or not _NAME_RE.match(stack):
            return None
        return cls(env, team, stack)

    @property
    def path(self) -> str:
        return f"{ENVIRONMENTS_DIR}/{self.environment}/{self.team}/{self.stack}"

    @property
    def key(self) -> str:
        """``<env>/<team>/<stack>``: the form used in depends_on and state keys."""
        return f"{self.environment}/{self.team}/{self.stack}"

    @property
    def id(self) -> str:
        """Slug safe for artifact names and concurrency groups."""
        return f"{self.environment}-{self.team}-{self.stack}"


@dataclass(frozen=True)
class ModulePin:
    """A module instance pinned in iacp.stack.json: release tag and resolved commit."""

    ref: str
    sha: str


@dataclass(frozen=True)
class StackMetadata:
    """Parsed iacp.stack.json for one stack root."""

    ref: StackRef
    region: str
    lifecycle: str
    backend: str
    request_id: str
    depends_on: tuple[str, ...] = ()
    modules: Mapping[str, ModulePin] = field(default_factory=dict)

    @classmethod
    def from_json(cls, ref: StackRef, data: Mapping[str, Any]) -> StackMetadata:
        """Build metadata, rejecting files whose identity disagrees with the path."""
        if not isinstance(data, Mapping):
            raise StackMetadataError(f"{ref.path}/{STACK_FILE}: must be a JSON object")
        declared = tuple(data.get(k) for k in ("environment", "team", "stack"))
        if declared != (ref.environment, ref.team, ref.stack):
            raise StackMetadataError(
                f"{ref.path}/{STACK_FILE}: identity {'/'.join(map(str, declared))} does not match the directory {ref.key}"
            )
        missing = [k for k in ("region", "lifecycle", "backend") if not isinstance(data.get(k), str) or not data.get(k)]
        if missing:
            raise StackMetadataError(f"{ref.path}/{STACK_FILE}: missing {', '.join(missing)}")
        modules = {
            name: ModulePin(ref=str(pin.get("ref", "")), sha=str(pin.get("sha", "")))
            for name, pin in (data.get("modules") or {}).items()
        }
        return cls(
            ref=ref,
            region=data["region"],
            lifecycle=data["lifecycle"],
            backend=data["backend"],
            request_id=str(data.get("request_id", "")),
            depends_on=tuple(str(d) for d in data.get("depends_on") or ()),
            modules=modules,
        )

    def matrix_entry(self) -> dict[str, Any]:
        """One leg of the GitHub Actions matrix."""
        return {
            "id": self.ref.id,
            "path": self.ref.path,
            "environment": self.ref.environment,
            "team": self.ref.team,
            "stack": self.ref.stack,
            "region": self.region,
            "lifecycle": self.lifecycle,
            "backend": self.backend,
        }
