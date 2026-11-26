"""Value types shared by the release gate components. All immutable."""

from __future__ import annotations

import enum
import re
from dataclasses import dataclass, field
from typing import Any

_SEMVER = re.compile(
    r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)


class Bump(enum.IntEnum):
    """Size of a version change, ordered so ``max()`` picks the largest."""

    NONE = 0
    PATCH = 1
    MINOR = 2
    MAJOR = 3

    @property
    def label(self) -> str:
        return self.name.lower()


@dataclass(frozen=True, order=False)
class SemVer:
    """A Semantic Version (build metadata is accepted and ignored)."""

    major: int
    minor: int
    patch: int
    prerelease: tuple[str, ...] = ()

    @classmethod
    def parse(cls, text: str) -> "SemVer":
        match = _SEMVER.match(str(text).strip())
        if not match:
            raise ValueError(f"not a semantic version: {text!r}")
        major, minor, patch, pre = match.groups()
        return cls(int(major), int(minor), int(patch), tuple(pre.split(".")) if pre else ())

    @classmethod
    def try_parse(cls, text: str) -> "SemVer | None":
        try:
            return cls.parse(text)
        except ValueError:
            return None

    @property
    def core(self) -> tuple[int, int, int]:
        return (self.major, self.minor, self.patch)

    @property
    def is_prerelease(self) -> bool:
        return bool(self.prerelease)

    def _key(self) -> tuple:
        # SemVer 2.0.0 section 11: a prerelease sorts before its release; numeric
        # identifiers sort numerically and before alphanumeric ones.
        pre = tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in self.prerelease)
        return (self.core, 0 if self.prerelease else 1, pre)

    def __lt__(self, other: "SemVer") -> bool:
        return self._key() < other._key()

    def __le__(self, other: "SemVer") -> bool:
        return self._key() <= other._key()

    def __gt__(self, other: "SemVer") -> bool:
        return self._key() > other._key()

    def __ge__(self, other: "SemVer") -> bool:
        return self._key() >= other._key()

    def __str__(self) -> str:
        base = f"{self.major}.{self.minor}.{self.patch}"
        return f"{base}-{'.'.join(self.prerelease)}" if self.prerelease else base


@dataclass(frozen=True)
class Variable:
    """The consumer-facing contract of one Terraform ``variable`` block."""

    name: str
    type: str = "any"
    required: bool = True
    default: Any = None
    nullable: bool = True
    validations: tuple[str, ...] = ()


@dataclass(frozen=True)
class Output:
    """The consumer-facing contract of one Terraform ``output`` block."""

    name: str


@dataclass(frozen=True)
class Interface:
    """A module's public interface: its variables and outputs keyed by name."""

    variables: dict[str, Variable] = field(default_factory=dict)
    outputs: dict[str, Output] = field(default_factory=dict)


class Subject(str, enum.Enum):
    VARIABLE = "variable"
    OUTPUT = "output"


@dataclass(frozen=True)
class Change:
    """One interface difference and the minimum bump it requires."""

    subject: Subject
    name: str
    description: str
    bump: Bump

    def render(self) -> str:
        return f"[{self.bump.label}] {self.subject.value} '{self.name}': {self.description}"
