"""Verify what ``terraform init`` actually installed against iacp.stack.json.

``verify-pins`` checks the pins recorded in iacp.stack.json before init. This
step runs after init and closes the gap between those pins and the module calls
Terraform really fetched:

* the root module calls in ``.terraform/modules/modules.json`` and the pins in
  iacp.stack.json must match one to one (none missing, none extra);
* each call's source must be exactly the generator's pinned form for its pin
  (``git::<repo>//modules/<name>?ref=modules/<name>/vX.Y.Z``);
* nested module calls inside platform modules may only use local paths;
* each fetched checkout's ``HEAD`` must be the commit recorded for its pin, which
  also covers a tag moved between ``ls-remote`` and ``init``.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Protocol, Sequence

from .models import ModulePin, StackMetadata
from .module_pins import PinError, parse_tag

MODULES_DIR = Path(".terraform") / "modules"
MANIFEST = MODULES_DIR / "modules.json"


@dataclass(frozen=True)
class InstalledModule:
    """One entry of .terraform/modules/modules.json."""

    key: str
    source: str

    @property
    def is_root_call(self) -> bool:
        """A module block in the stack's own configuration (not inside another module)."""
        return bool(self.key) and "." not in self.key

    @property
    def is_nested(self) -> bool:
        return "." in self.key


# ---- pure functions ---------------------------------------------------------


def parse_manifest(data: Mapping[str, Any]) -> tuple[InstalledModule, ...]:
    """Entries of modules.json, without the root module itself (Key "")."""
    if not isinstance(data, Mapping) or not isinstance(data.get("Modules"), list):
        raise PinError(f"{MANIFEST}: not a Terraform module manifest")
    return tuple(
        InstalledModule(key=str(m.get("Key", "")), source=str(m.get("Source", "")))
        for m in data["Modules"]
        if m.get("Key")
    )


def is_local_source(source: str) -> bool:
    return source.startswith(("./", "../"))


def expected_source(repo_url: str, pin: ModulePin) -> str:
    """The source the generator renders for pin (catalog.SourceAddress)."""
    name, _ = parse_tag(pin.ref)
    return f"git::{repo_url}//modules/{name}?ref={pin.ref}"


def manifest_problems(meta: StackMetadata, installed: Sequence[InstalledModule], repo_url: str) -> list[str]:
    """Every way the installed module calls disagree with the stack's pins (empty when they match)."""
    roots = {m.key: m for m in installed if m.is_root_call}
    pins = dict(meta.modules)
    problems = [f"module.{key}: installed from {roots[key].source!r} but has no pin in iacp.stack.json" for key in sorted(roots.keys() - pins.keys())]
    problems += [f"module.{key}: pinned in iacp.stack.json but not a module call in the stack" for key in sorted(pins.keys() - roots.keys())]
    for key in sorted(roots.keys() & pins.keys()):
        try:
            want = expected_source(repo_url, pins[key])
        except PinError as exc:
            problems.append(f"module.{key}: {exc}")
            continue
        if roots[key].source != want:
            problems.append(f"module.{key}: source {roots[key].source!r} does not match its pin (want {want!r})")
    problems += [
        f"module.{m.key}: nested module call uses non-local source {m.source!r}"
        for m in sorted(installed, key=lambda m: m.key)
        if m.is_nested and not is_local_source(m.source)
    ]
    return problems


# ---- adapter boundary -----------------------------------------------------------


class HeadPort(Protocol):
    def head_commit(self, git_dir: Path) -> str | None: ...


class ModuleInstallVerifier:
    """Checks a stack's installed modules: manifest against pins, then fetched commits."""

    def __init__(self, git: HeadPort, repo_url: str) -> None:
        self.git = git
        self.repo_url = repo_url

    def verify(self, stack_dir: Path, meta: StackMetadata, installed: Sequence[InstalledModule]) -> list[str]:
        """Human-readable confirmations; raises PinError listing every problem."""
        problems = manifest_problems(meta, installed, self.repo_url)
        if not problems:
            problems = [p for key in sorted(meta.modules) if (p := self._commit_problem(stack_dir, key, meta.modules[key]))]
        if problems:
            raise PinError(f"{meta.ref.key}: installed modules do not match iacp.stack.json:\n  - " + "\n  - ".join(problems))
        return [f"{meta.ref.key}: module.{key} {pin.ref} installed at {pin.sha[:12]}" for key, pin in sorted(meta.modules.items())]

    def _commit_problem(self, stack_dir: Path, key: str, pin: ModulePin) -> str | None:
        git_dir = Path(stack_dir) / MODULES_DIR / key / ".git"
        if not git_dir.is_dir():
            return f"module.{key}: {git_dir} is missing, so the fetched commit cannot be checked"
        head = self.git.head_commit(git_dir)
        if head != pin.sha:
            return f"module.{key}: fetched commit {head} is not the recorded {pin.sha}"
        return None
