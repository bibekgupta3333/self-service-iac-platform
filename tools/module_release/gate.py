"""ReleaseGate: gathers inputs through the adapters and applies the pure decision."""

from __future__ import annotations

from .diffing import diff_interfaces, is_release_relevant
from .interface import InterfaceError, parse_interface
from .models import Interface, SemVer
from .sources import GitHistory, ModuleWorkspace
from .versioning import GateInput, GateResult, evaluate, git_tag, latest_release


class ReleaseGate:
    """Compares a module at HEAD (working tree) with its last released tag."""

    def __init__(self, workspace: ModuleWorkspace, git: GitHistory) -> None:
        self.workspace = workspace
        self.git = git

    def previous_version(self, module: str) -> SemVer | None:
        return latest_release(module, self.git.tags(f"modules/{module}/v*"))

    def declared_version(self, module: str) -> SemVer:
        version = (self.workspace.manifest(module).get("metadata") or {}).get("version")
        if version is None:
            raise ValueError(f"modules/{module}/module.yaml has no metadata.version")
        return SemVer.parse(str(version))

    def interfaces(self, module: str, previous: SemVer) -> tuple[Interface, Interface]:
        tag = git_tag(module, previous)
        old = parse_interface(self.git.files_at(tag, self.workspace.relative_dir(module)))
        new = parse_interface(self.workspace.terraform_files(module))
        return old, new

    def check(self, module: str) -> GateResult:
        declared = self.declared_version(module)
        previous = self.previous_version(module)
        if previous is None:
            return evaluate(GateInput(module, declared, None))
        tag = git_tag(module, previous)
        relative = self.workspace.relative_dir(module)
        content_changed = any(is_release_relevant(p) for p in self.git.changed_files(tag, relative))
        try:
            old, new = self.interfaces(module, previous)
        except InterfaceError as exc:
            return evaluate(GateInput(module, declared, previous, (), content_changed, (str(exc),)))
        return evaluate(GateInput(module, declared, previous, tuple(diff_interfaces(old, new)), content_changed))
