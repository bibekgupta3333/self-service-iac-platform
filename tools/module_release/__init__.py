"""SemVer interface-diff gate, release notes and catalog indexing for modules/<name>."""

from .catalog import CatalogClient, CatalogError, IndexResult, build_index_payload, index_url
from .diffing import constraint_floor, diff_interfaces, diff_requirements, is_release_relevant, required_bump
from .gate import ReleaseGate
from .interface import InterfaceError, parse_interface
from .models import Bump, Change, Interface, Output, SemVer, Subject, Variable
from .notes import changelog_section, diff_summary, release_notes
from .sources import GitError, GitRepository, ModuleWorkspace, modules_in_paths
from .versioning import GateInput, GateResult, bump_between, evaluate, git_tag, latest_release

__all__ = [
    "Bump",
    "CatalogClient",
    "CatalogError",
    "Change",
    "GateInput",
    "GateResult",
    "GitError",
    "GitRepository",
    "IndexResult",
    "Interface",
    "InterfaceError",
    "ModuleWorkspace",
    "Output",
    "ReleaseGate",
    "SemVer",
    "Subject",
    "Variable",
    "build_index_payload",
    "bump_between",
    "changelog_section",
    "constraint_floor",
    "diff_interfaces",
    "diff_requirements",
    "diff_summary",
    "evaluate",
    "git_tag",
    "index_url",
    "is_release_relevant",
    "latest_release",
    "modules_in_paths",
    "parse_interface",
    "release_notes",
    "required_bump",
]
