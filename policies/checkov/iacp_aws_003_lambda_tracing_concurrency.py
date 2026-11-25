"""IACP_AWS_003: Lambda functions enable X-Ray tracing and set reserved concurrency.

Matches the lambda module's secure defaults in docs/04-module-registry.md
("X-Ray active", "reserved concurrency set").
"""

from __future__ import annotations

from typing import Any

from _conf import blocks, first
from checkov.common.models.enums import CheckCategories, CheckResult
from checkov.terraform.checks.resource.base_resource_check import BaseResourceCheck


class LambdaTracingAndReservedConcurrency(BaseResourceCheck):
    def __init__(self) -> None:
        super().__init__(
            name="Ensure Lambda functions use active X-Ray tracing and reserved concurrency",
            id="IACP_AWS_003",
            categories=[CheckCategories.LOGGING],
            supported_resources=("aws_lambda_function",),
            guideline='Add tracing_config { mode = "Active" } and set reserved_concurrent_executions >= 0',
        )

    def scan_resource_conf(self, conf: dict[str, Any]) -> CheckResult:
        modes = [first(t, "mode") for t in blocks(conf, "tracing_config")]
        if "Active" not in modes:
            self.details.append('tracing_config.mode must be "Active"')
            return CheckResult.FAILED

        concurrency = first(conf, "reserved_concurrent_executions")
        if concurrency is None:
            self.details.append("reserved_concurrent_executions is not set")
            return CheckResult.FAILED
        try:
            value = int(concurrency)
        except (TypeError, ValueError):
            return CheckResult.PASSED  # set from an expression; the module validates its range
        if value < 0:  # -1 means "unreserved"
            self.details.append("reserved_concurrent_executions is -1 (unreserved)")
            return CheckResult.FAILED
        return CheckResult.PASSED

    def get_evaluated_keys(self) -> list[str]:
        return ["tracing_config/[0]/mode", "reserved_concurrent_executions"]


check = LambdaTracingAndReservedConcurrency()
