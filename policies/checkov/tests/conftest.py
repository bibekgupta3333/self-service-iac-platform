"""Shared fixtures: run Checkov with only the IACP_* custom checks on tiny HCL dirs."""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path

import pytest
from checkov.runner_filter import RunnerFilter
from checkov.terraform.checks.resource.registry import resource_registry
from checkov.terraform.runner import Runner

CHECKS_DIR = Path(__file__).resolve().parents[1]


@pytest.fixture(scope="session", autouse=True)
def _load_custom_checks() -> None:
    # Register once per session; loading twice would register every check twice.
    resource_registry.load_external_checks(str(CHECKS_DIR))


Results = dict[str, set[str]]


@pytest.fixture
def scan(tmp_path: Path) -> Callable[[str, str], Results]:
    """Write ``hcl`` to a temp dir, run one check, return resource ids by outcome."""

    def _scan(check_id: str, hcl: str) -> Results:
        (tmp_path / "main.tf").write_text(hcl)
        report = Runner().run(
            root_folder=str(tmp_path),
            runner_filter=RunnerFilter(framework=["terraform"], checks=[check_id]),
        )
        return {
            "passed": {c.resource for c in report.passed_checks if c.check_id == check_id},
            "failed": {c.resource for c in report.failed_checks if c.check_id == check_id},
            "skipped": {c.resource for c in report.skipped_checks if c.check_id == check_id},
        }

    return _scan
