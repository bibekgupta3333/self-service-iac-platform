"""Value types for stack roots under ``environments/<env>/<team>/<stack>/``."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any, Mapping

ENVIRONMENTS_DIR = "environments"
STACK_FILE = "iacp.stack.json"
ENVIRONMENTS = ("development", "staging", "production")

# Same identity rule as backend/internal/generator (nameRe). Names never contain
# "_", which is what makes StackRef.id injective.
_NAME_RE = re.compile(r"[a-z][a-z0-9-]{1,40}")
ID_SEPARATOR = "__"

# Files under environments/ that are not part of any stack root.
_NON_STACK_FILE_RE = re.compile(r"environments/README\.md|environments/(development|staging|production)/\.gitkeep")


def is_environments_path(path: str) -> bool:
    """True for any repo-relative path under environments/."""
    return _normalize(path).startswith(f"{ENVIRONMENTS_DIR}/")


def is_non_stack_file(path: str) -> bool:
    """True for the allow-listed files under environments/ that belong to no stack."""
    return _NON_STACK_FILE_RE.fullmatch(_normalize(path)) is not None


def _normalize(path: str) -> str:
    return path.replace("\\", "/").strip("/")


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
        parts = _normalize(path).split("/")
        if len(parts) < 5 or parts[0] != ENVIRONMENTS_DIR:
            return None
        env, team, stack = parts[1:4]
        if env not in ENVIRONMENTS or not _NAME_RE.fullmatch(team) or not _NAME_RE.fullmatch(stack):
            return None
        return cls(env, team, stack)

    @classmethod
    def from_root(cls, path: str) -> StackRef | None:
        """The stack for a stack *root* path (``environments/<env>/<team>/<stack>``), else None.

        Unlike ``from_path`` it accepts nothing deeper, so a path that names a file or
        another directory inside a stack is not mistaken for the root.
        """
        root = _normalize(path)
        ref = cls.from_path(root + "/_")
        return ref if ref is not None and ref.path == root else None

    @property
    def path(self) -> str:
        return f"{ENVIRONMENTS_DIR}/{self.environment}/{self.team}/{self.stack}"

    @property
    def key(self) -> str:
        """``<env>/<team>/<stack>``: the form used in depends_on and state keys."""
        return f"{self.environment}/{self.team}/{self.stack}"

    @property
    def id(self) -> str:
        """Slug for artifact names, concurrency groups and comment markers.

        Joined with "__", which names cannot contain, so distinct stacks never
        share an id (``a-b/c`` and ``a/b-c`` would collide with a single "-").
        """
        return ID_SEPARATOR.join((self.environment, self.team, self.stack))


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
    fencing_token: int | None = None
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
        token = data.get("fencing_token")
        return cls(
            ref=ref,
            region=data["region"],
            lifecycle=data["lifecycle"],
            backend=data["backend"],
            request_id=str(data.get("request_id", "")),
            # The token of the request that rendered the stack (validate reports carry it).
            fencing_token=token if isinstance(token, int) and not isinstance(token, bool) else None,
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
