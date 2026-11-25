"""Checks run against each module package.

Each check is a small class with a ``check(package) -> list[Finding]``
method, so new rules can be added without touching the orchestrator.
"""

from __future__ import annotations

import abc
import json
from pathlib import Path
from typing import Iterable

from jsonschema import Draft202012Validator, FormatChecker

from . import diffing, hcl
from .models import Finding, ModulePackage

# Inputs the platform injects into every module; they are in variables.tf
# but deliberately absent from module.yaml inputs.
INJECTED_INPUTS = frozenset({"team", "environment", "stack"})


class Check(abc.ABC):
    """Base class for a module check."""

    code: str = "check"

    @abc.abstractmethod
    def check(self, package: ModulePackage) -> list[Finding]:
        """Return the findings for one module."""

    def finding(self, package: ModulePackage, message: str) -> Finding:
        return Finding(module=package.name, code=self.code, message=message)


class SchemaValidator(Check):
    """Validates module.yaml against modules/_shared/module.schema.json."""

    code = "schema"

    def __init__(self, schema: dict) -> None:
        Draft202012Validator.check_schema(schema)
        self._validator = Draft202012Validator(schema, format_checker=FormatChecker())

    @classmethod
    def from_file(cls, path: Path) -> "SchemaValidator":
        return cls(json.loads(Path(path).read_text(encoding="utf-8")))

    def check(self, package: ModulePackage) -> list[Finding]:
        if package.manifest is None:
            return [self.finding(package, package.load_error or "manifest missing")]
        errors = sorted(self._validator.iter_errors(package.manifest), key=lambda e: list(e.absolute_path))
        return [
            self.finding(package, f"{diffing.json_path(e.absolute_path)}: {e.message}") for e in errors
        ]


class MetadataChecker(Check):
    """metadata.name must equal the directory name."""

    code = "metadata"

    def check(self, package: ModulePackage) -> list[Finding]:
        name = _get(package.manifest, "metadata", "name")
        if name is not None and name != package.name:
            return [self.finding(package, f"metadata.name '{name}' does not match directory '{package.name}'")]
        return []


class VariableParityChecker(Check):
    """module.yaml inputs and variables.tf must declare the same names and compatible types."""

    code = "input-parity"

    def __init__(self, injected: Iterable[str] = INJECTED_INPUTS) -> None:
        self.injected = frozenset(injected)

    def check(self, package: ModulePackage) -> list[Finding]:
        properties = _get(package.manifest, "inputs", "properties")
        if not isinstance(properties, dict):
            return []
        tf_vars = {k: v for k, v in package.variables.items() if k not in self.injected}
        only_manifest, only_tf = diffing.key_diff(properties, tf_vars)
        findings = []
        if only_manifest:
            findings.append(
                self.finding(package, f"inputs missing from variables.tf: {diffing.format_names(only_manifest)}")
            )
        if only_tf:
            findings.append(
                self.finding(package, f"variables missing from module.yaml inputs: {diffing.format_names(only_tf)}")
            )
        for name in sorted(set(properties) & set(tf_vars)):
            prop = properties[name] if isinstance(properties[name], dict) else {}
            schema_types = diffing.json_types(prop)
            if not diffing.types_compatible(tf_vars[name], schema_types):
                findings.append(
                    self.finding(
                        package,
                        f"input '{name}': module.yaml type {sorted(schema_types)} "
                        f"is not compatible with Terraform type '{tf_vars[name]}'",
                    )
                )
        missing_injected = sorted(self.injected - set(package.variables))
        if package.variables and missing_injected:
            findings.append(
                self.finding(package, f"platform-injected variables missing: {diffing.format_names(missing_injected)}")
            )
        return findings


class OutputParityChecker(Check):
    """Every output listed in module.yaml must exist in outputs.tf, and vice versa."""

    code = "output-parity"

    def check(self, package: ModulePackage) -> list[Finding]:
        outputs = _get(package.manifest, "outputs")
        if not isinstance(outputs, list):
            return []
        declared = [o.get("name") for o in outputs if isinstance(o, dict) and o.get("name")]
        only_manifest, only_tf = diffing.key_diff(declared, package.outputs)
        findings = []
        if only_manifest:
            findings.append(
                self.finding(package, f"outputs missing from outputs.tf: {diffing.format_names(only_manifest)}")
            )
        if only_tf:
            findings.append(
                self.finding(package, f"outputs missing from module.yaml: {diffing.format_names(only_tf)}")
            )
        return findings


class VersionStampChecker(Check):
    """``local.module_version`` must equal metadata.version (feeds the iacp_metadata output)."""

    code = "version-stamp"

    def check(self, package: ModulePackage) -> list[Finding]:
        version = _get(package.manifest, "metadata", "version")
        if version is None or not package.variables:
            return []
        stamped = hcl.string_attribute(package.locals_body, "module_version")
        if stamped is None:
            return [self.finding(package, "locals.module_version is not set")]
        if stamped != version:
            return [
                self.finding(package, f"locals.module_version '{stamped}' does not match metadata.version '{version}'")
            ]
        return []


def _get(data, *keys):
    for key in keys:
        if not isinstance(data, dict):
            return None
        data = data.get(key)
    return data
