"""Pure SemVer gate decision: is the declared version a big enough bump?"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Iterable

from .diffing import required_bump
from .models import Bump, Change, SemVer

TAG_PREFIX = "modules/{name}/v"


def git_tag(module: str, version: SemVer) -> str:
    """The release tag for ``module`` at ``version``: ``modules/<name>/vX.Y.Z``."""
    return f"{TAG_PREFIX.format(name=module)}{version}"


def version_from_tag(module: str, tag: str) -> SemVer | None:
    """Parse the version out of a ``modules/<name>/vX.Y.Z`` tag; None if it is not one."""
    prefix = TAG_PREFIX.format(name=module)
    return SemVer.try_parse(tag[len(prefix):]) if tag.startswith(prefix) else None


def latest_release(module: str, tags: Iterable[str]) -> SemVer | None:
    """Highest final (non-prerelease) version among the module's tags."""
    versions = [v for v in (version_from_tag(module, t) for t in tags) if v and not v.is_prerelease]
    return max(versions, default=None)


def bump_between(previous: SemVer, declared: SemVer) -> Bump:
    """Which core component increased from ``previous`` to ``declared`` (NONE if none did)."""
    if declared.major != previous.major:
        return Bump.MAJOR if declared.major > previous.major else Bump.NONE
    if declared.minor != previous.minor:
        return Bump.MINOR if declared.minor > previous.minor else Bump.NONE
    if declared.patch != previous.patch:
        return Bump.PATCH if declared.patch > previous.patch else Bump.NONE
    return Bump.NONE


def next_versions(previous: SemVer) -> dict[Bump, tuple[int, int, int]]:
    """The canonical next core version for each bump size."""
    major, minor, patch = previous.core
    return {
        Bump.MAJOR: (major + 1, 0, 0),
        Bump.MINOR: (major, minor + 1, 0),
        Bump.PATCH: (major, minor, patch + 1),
    }


def effective_requirement(required: Bump, previous: SemVer) -> Bump:
    """Below 1.0.0 anything may change (SemVer item 4): a breaking change needs only a minor bump."""
    return Bump.MINOR if previous.major == 0 and required is Bump.MAJOR else required


@dataclass(frozen=True)
class GateResult:
    """The gate decision for one module."""

    module: str
    declared: SemVer
    previous: SemVer | None
    required: Bump
    actual: Bump
    changes: tuple[Change, ...] = ()
    errors: tuple[str, ...] = ()
    warnings: tuple[str, ...] = ()
    content_changed: bool = False

    @property
    def ok(self) -> bool:
        return not self.errors

    @property
    def first_release(self) -> bool:
        return self.previous is None

    @property
    def releasable(self) -> bool:
        """True when HEAD declares a version newer than the last final release."""
        return self.ok and (self.previous is None or self.declared > self.previous)

    @property
    def tag(self) -> str:
        return git_tag(self.module, self.declared)

    def to_dict(self) -> dict:
        return {
            "module": self.module,
            "ok": self.ok,
            "declared": str(self.declared),
            "previous": str(self.previous) if self.previous else None,
            "tag": self.tag,
            "required_bump": self.required.label,
            "actual_bump": self.actual.label,
            "releasable": self.releasable,
            "content_changed": self.content_changed,
            "changes": [
                {"subject": c.subject.value, "name": c.name, "description": c.description, "bump": c.bump.label}
                for c in self.changes
            ],
            "errors": list(self.errors),
            "warnings": list(self.warnings),
        }


@dataclass(frozen=True)
class GateInput:
    """Everything the decision needs, gathered by the I/O adapters."""

    module: str
    declared: SemVer
    previous: SemVer | None
    changes: tuple[Change, ...] = ()
    content_changed: bool = False
    extra_errors: tuple[str, ...] = field(default=())


def evaluate(gate: GateInput) -> GateResult:
    """Decide whether ``gate.declared`` is an acceptable version. Pure."""
    errors = list(gate.extra_errors)
    warnings: list[str] = []
    changes = tuple(gate.changes)
    previous, declared = gate.previous, gate.declared

    if previous is None:
        return GateResult(gate.module, declared, None, Bump.NONE, Bump.NONE, changes,
                          tuple(errors), ("first release: no previous tag to compare against",), gate.content_changed)

    required = required_bump(changes, gate.content_changed)
    actual = bump_between(previous, declared)
    effective = effective_requirement(required, previous)

    if declared < previous:
        errors.append(f"version {declared} is lower than the last release {previous}")
    elif declared.core == previous.core and not declared.is_prerelease:
        if required is not Bump.NONE:
            errors.append(
                f"module changed since v{previous} but metadata.version was not bumped "
                f"(at least a {effective.label} bump is required)"
            )
    elif actual < effective:
        errors.append(
            f"version {previous} -> {declared} is a {actual.label} bump, "
            f"but the changes require at least a {effective.label} bump"
        )
    if declared > previous and actual is not Bump.NONE:
        if declared.core != next_versions(previous)[actual]:
            expected = ".".join(map(str, next_versions(previous)[actual]))
            warnings.append(f"{declared} skips versions; the next {actual.label} version is {expected}")
        if required is Bump.NONE:
            warnings.append("version bumped although nothing release-relevant changed")
    if effective is not required:
        warnings.append("0.x module: breaking changes need only a minor bump")
    return GateResult(gate.module, declared, previous, effective, actual, changes,
                      tuple(errors), tuple(warnings), gate.content_changed)
