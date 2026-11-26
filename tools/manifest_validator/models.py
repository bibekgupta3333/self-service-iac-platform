"""Data types shared by the validator components."""

from __future__ import annotations

import enum
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


class Severity(str, enum.Enum):
    """How a finding affects the exit status."""

    ERROR = "error"
    WARNING = "warning"


@dataclass(frozen=True)
class Finding:
    """One problem found in one module."""

    module: str
    code: str
    message: str
    severity: Severity = Severity.ERROR

    def render(self) -> str:
        return f"[{self.severity.value}] {self.module}: {self.code}: {self.message}"


@dataclass
class ModulePackage:
    """A module directory with its parsed manifest and Terraform interface."""

    name: str
    path: Path
    manifest: dict[str, Any] | None = None
    load_error: str | None = None
    variables: dict[str, str] = field(default_factory=dict)
    outputs: dict[str, str] = field(default_factory=dict)
    locals_body: str = ""
