"""Runs every check over every module and builds a report."""

from __future__ import annotations

from pathlib import Path
from typing import Sequence

from .checks import (
    Check,
    CompatibilityParityChecker,
    InputsSchemaChecker,
    MetadataChecker,
    OutputParityChecker,
    SchemaValidator,
    VariableParityChecker,
    VersionStampChecker,
)
from .loader import ManifestLoader
from .models import Finding, ModulePackage
from .report import Report

SCHEMA_RELATIVE_PATH = Path("_shared") / "module.schema.json"


class ManifestValidator:
    """Validates all modules under a modules/ root."""

    def __init__(self, loader: ManifestLoader, checks: Sequence[Check]) -> None:
        self.loader = loader
        self.checks = list(checks)

    @classmethod
    def for_root(cls, modules_root: Path, schema_path: Path | None = None) -> "ManifestValidator":
        modules_root = Path(modules_root)
        schema = SchemaValidator.from_file(schema_path or modules_root / SCHEMA_RELATIVE_PATH)
        checks = [
            schema,
            InputsSchemaChecker(),
            MetadataChecker(),
            VariableParityChecker(),
            OutputParityChecker(),
            CompatibilityParityChecker(),
            VersionStampChecker(),
        ]
        return cls(ManifestLoader(modules_root), checks)

    def validate_package(self, package: ModulePackage) -> list[Finding]:
        if package.manifest is None:
            return [Finding(package.name, "manifest", package.load_error or "manifest missing")]
        return [finding for check in self.checks for finding in check.check(package)]

    def run(self) -> Report:
        report = Report()
        for package in self.loader.discover():
            report.add_module(package.name, self.validate_package(package))
        return report
