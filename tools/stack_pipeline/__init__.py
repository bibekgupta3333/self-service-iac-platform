"""CI helpers for stack roots: changed-stack detection, policy context and plan summaries."""

from .detection import (
    DetectionError,
    DetectionPolicy,
    DetectionResult,
    StackDetector,
    check_guard_rails,
    dependency_conflicts,
    stack_roots,
    triggers_full_run,
    unmapped_paths,
)
from .models import ModulePin, StackMetadata, StackMetadataError, StackRef

__all__ = [
    "DetectionError",
    "DetectionPolicy",
    "DetectionResult",
    "ModulePin",
    "StackDetector",
    "StackMetadata",
    "StackMetadataError",
    "StackRef",
    "check_guard_rails",
    "dependency_conflicts",
    "stack_roots",
    "triggers_full_run",
    "unmapped_paths",
]
