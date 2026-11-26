from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Callable

import pytest

from tools.stack_pipeline.models import StackMetadata, StackRef


def stack_json(env: str, team: str, stack: str, /, **overrides: Any) -> dict[str, Any]:
    """An iacp.stack.json document shaped like the generator's output."""
    doc: dict[str, Any] = {
        "backend": "local",
        "depends_on": [],
        "environment": env,
        "fencing_token": 1,
        "lifecycle": "active",
        "modules": {"bucket": {"ref": "modules/s3/v1.0.0", "sha": "a" * 40}},
        "region": "us-east-1",
        "request_id": "req-test01",
        "stack": stack,
        "team": team,
    }
    doc.update(overrides)
    return doc


def metadata(env: str, team: str, stack: str, /, **overrides: Any) -> StackMetadata:
    return StackMetadata.from_json(StackRef(env, team, stack), stack_json(env, team, stack, **overrides))


@pytest.fixture
def make_stack(tmp_path: Path) -> Callable[..., Path]:
    """Write environments/<env>/<team>/<stack>/iacp.stack.json under tmp_path."""

    def _make(env: str, team: str, stack: str, /, **overrides: Any) -> Path:
        root = tmp_path / "environments" / env / team / stack
        root.mkdir(parents=True, exist_ok=True)
        (root / "iacp.stack.json").write_text(json.dumps(stack_json(env, team, stack, **overrides)))
        (root / "main.tf").write_text("# generated\n")
        return root

    return _make


class FakeRepository:
    """In-memory StackRepository for detector tests."""

    def __init__(self, *stacks: StackMetadata, orphans: tuple[StackRef, ...] = ()) -> None:
        self.stacks = {s.ref: s for s in stacks}
        self.orphans = set(orphans)  # roots with files but no iacp.stack.json

    def load(self, ref: StackRef) -> StackMetadata | None:
        return self.stacks.get(ref)

    def has_files(self, ref: StackRef) -> bool:
        return ref in self.stacks or ref in self.orphans

    def all_refs(self) -> list[StackRef]:
        return sorted(self.stacks)
