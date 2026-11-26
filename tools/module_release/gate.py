"""ReleaseGate: gathers inputs through the adapters and applies the pure decision."""

from __future__ import annotations

from .diffing import diff_interfaces, is_release_relevant
from .interface import InterfaceError, parse_interface
from .models import Interface, SemVer
from .sources import GitHistory, ModuleWorkspace
from .versioning import GateInput, GateResult, evaluate, git_tag, latest_release, tagged_versions


class ReleaseGate:
    """Compares a module at HEAD (working tree) with its last released tag."""

    def __init__(self, workspace: ModuleWorkspace, git: GitHistory) -> None:
        self.workspace = workspace
        self.git = git

    def module_tags(self, module: str) -> list[str]:
        return self.git.tags(f"modules/{module}/v*")

    def previous_version(self, module: str, below: SemVer | None = None) -> SemVer | None:
        return latest_release(module, self.module_tags(module), below)

    def changed_since(self, module: str, version: SemVer) -> bool:
        """True if release-relevant files differ from the module at tag ``version``."""
        changed = self.git.changed_files(git_tag(module, version), self.workspace.relative_dir(module))
        return any(is_release_relevant(p) for p in changed)

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

    def check(self, module: str, before_declared: bool = False) -> GateResult:
        """Gate ``module``.

        ``before_declared`` compares against the last final release below the
        declared version, which is what release notes need when the declared
        version is already tagged (a re-run).
        """
        declared = self.declared_version(module)
        tagged = tagged_versions(module, self.module_tags(module))
        previous = self.previous_version(module, declared if before_declared else None)
        since_declared = declared in tagged and self.changed_since(module, declared)
        extra = {"tagged": tagged, "changed_since_declared": since_declared}
        if previous is None:
            return evaluate(GateInput(module, declared, None, **extra))
        content_changed = self.changed_since(module, previous)
        try:
            old, new = self.interfaces(module, previous)
        except InterfaceError as exc:
            return evaluate(GateInput(module, declared, previous, (), content_changed, (str(exc),), **extra))
        return evaluate(GateInput(module, declared, previous, tuple(diff_interfaces(old, new)), content_changed,
                                  **extra))
