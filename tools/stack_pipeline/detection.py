"""Changed-stack detection (docs/05-automation-pipeline.md section 3).

The mapping and the guard rails are pure functions over paths and metadata.
``StackDetector`` composes them with a ``StackRepository`` adapter, which is the
only part that reads the working tree.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Iterable, Protocol, Sequence

from .models import StackMetadata, StackRef

DEFAULT_MAX_STACKS = 20


class DetectionError(Exception):
    """A guard rail rejected the change set."""


class StackRepository(Protocol):
    """Reads stack roots from somewhere (the working tree in production)."""

    def load(self, ref: StackRef) -> StackMetadata | None:
        """Metadata for ref, or None when the root has no iacp.stack.json (deleted)."""

    def all_refs(self) -> list[StackRef]:
        """Every stack root that has an iacp.stack.json."""


# ---- pure functions ---------------------------------------------------------


def stack_roots(paths: Iterable[str]) -> list[StackRef]:
    """Distinct stack roots touched by paths, sorted."""
    return sorted({ref for ref in map(StackRef.from_path, paths) if ref is not None})


def triggers_full_run(paths: Iterable[str], prefixes: Sequence[str]) -> bool:
    """True when any path is under a pipeline prefix (the pipeline itself changed)."""
    return any(path.startswith(prefix) for path in paths for prefix in prefixes if prefix)


def dependency_conflicts(stacks: Sequence[StackMetadata]) -> list[tuple[str, str]]:
    """Pairs (stack, dependency) where both changed in the same change set.

    depends_on entries may name a stack by ``<env>/<team>/<stack>`` or by its
    slug id; dependent stacks must be applied in separate requests (DAG order).
    """
    names = {alias: s.ref.key for s in stacks for alias in (s.ref.key, s.ref.id, s.ref.path)}
    return sorted(
        (s.ref.key, names[dep]) for s in stacks for dep in s.depends_on if dep in names and names[dep] != s.ref.key
    )


def check_guard_rails(stacks: Sequence[StackMetadata], max_stacks: int) -> None:
    """Raise DetectionError when the change set is too large or crosses a dependency edge."""
    if len(stacks) > max_stacks:
        raise DetectionError(
            f"{len(stacks)} stacks changed (limit {max_stacks}); bulk changes need the iacp/bulk label and platform approval"
        )
    conflicts = dependency_conflicts(stacks)
    if conflicts:
        pairs = ", ".join(f"{a} -> {b}" for a, b in conflicts)
        raise DetectionError(f"dependent stacks changed together ({pairs}); split them into separate requests")


# ---- result and orchestration -------------------------------------------------


@dataclass(frozen=True)
class DetectionResult:
    """Stacks to validate, plus the roots that were skipped because they were deleted."""

    stacks: tuple[StackMetadata, ...]
    skipped: tuple[StackRef, ...] = ()
    full_run: bool = False

    @property
    def count(self) -> int:
        return len(self.stacks)

    def matrix(self) -> list[dict[str, Any]]:
        return [s.matrix_entry() for s in self.stacks]

    def to_dict(self) -> dict[str, Any]:
        return {
            "count": self.count,
            "full_run": self.full_run,
            "stacks": self.matrix(),
            "skipped": [ref.path for ref in self.skipped],
        }


@dataclass(frozen=True)
class DetectionPolicy:
    """Tunables for detection."""

    max_stacks: int = DEFAULT_MAX_STACKS
    # Paths whose change re-validates every stack (the workflow, its scripts, this tool).
    full_run_prefixes: tuple[str, ...] = field(default_factory=tuple)


class StackDetector:
    """Turns a list of changed files into the stacks a workflow must validate."""

    def __init__(self, repository: StackRepository, policy: DetectionPolicy | None = None) -> None:
        self.repository = repository
        self.policy = policy or DetectionPolicy()

    def detect(self, changed_paths: Sequence[str]) -> DetectionResult:
        full_run = triggers_full_run(changed_paths, self.policy.full_run_prefixes)
        refs = sorted(set(stack_roots(changed_paths)) | (set(self.repository.all_refs()) if full_run else set()))
        loaded = [(ref, self.repository.load(ref)) for ref in refs]
        stacks = tuple(meta for _, meta in loaded if meta is not None)
        skipped = tuple(ref for ref, meta in loaded if meta is None)
        # A full run re-validates what exists; the size limit guards change sets, not the repo.
        check_guard_rails(stacks, len(stacks) if full_run else self.policy.max_stacks)
        return DetectionResult(stacks=stacks, skipped=skipped, full_run=full_run)
