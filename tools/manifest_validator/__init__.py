"""Validate module.yaml manifests against the registry schema and Terraform interface."""

from .checks import (
    INJECTED_INPUTS,
    MetadataChecker,
    OutputParityChecker,
    SchemaValidator,
    VariableParityChecker,
    VersionStampChecker,
)
from .loader import ManifestLoader
from .models import Finding, ModulePackage, Severity
from .report import Report
from .validator import ManifestValidator

__all__ = [
    "INJECTED_INPUTS",
    "Finding",
    "ManifestLoader",
    "ManifestValidator",
    "MetadataChecker",
    "ModulePackage",
    "OutputParityChecker",
    "Report",
    "SchemaValidator",
    "Severity",
    "VariableParityChecker",
    "VersionStampChecker",
]
