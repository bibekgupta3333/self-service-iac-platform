"""IACP_AWS_002: S3 server-side encryption must use a customer-managed KMS key.

Checks every ``aws_s3_bucket_server_side_encryption_configuration``: each rule
must default to ``aws:kms`` (or ``aws:kms:dsse``) and name ``kms_master_key_id``.
That every bucket *has* such a configuration is enforced on the plan by OPA
(``iacp.encryption.s3_kms``).
"""

from __future__ import annotations

from typing import Any

from checkov.common.models.enums import CheckCategories, CheckResult
from checkov.terraform.checks.resource.base_resource_check import BaseResourceCheck


# Helpers are inlined (not imported from a shared module): Checkov loads external
# checks for several frameworks in parallel threads, and a shared module import
# races with that and fails intermittently.
def _blocks(conf: dict[str, Any], key: str) -> list[dict[str, Any]]:
    """Nested blocks stored under ``key``, always as a list of dicts."""
    value = conf.get(key) or []
    if isinstance(value, dict):
        value = [value]
    return [b for b in value if isinstance(b, dict)]


def _first(conf: dict[str, Any], key: str, default: Any = None) -> Any:
    """Unwrap Checkov's single-element list for ``key`` (``default`` when absent)."""
    value = conf.get(key)
    if isinstance(value, list):
        return value[0] if value else default
    return default if value is None else value

KMS_ALGORITHMS = {"aws:kms", "aws:kms:dsse"}


class S3SseUsesCustomerManagedKms(BaseResourceCheck):
    def __init__(self) -> None:
        super().__init__(
            name="Ensure S3 server-side encryption uses aws:kms with a customer-managed key",
            id="IACP_AWS_002",
            categories=[CheckCategories.ENCRYPTION],
            supported_resources=("aws_s3_bucket_server_side_encryption_configuration",),
            guideline='Set sse_algorithm = "aws:kms" and kms_master_key_id = <module KMS key ARN>',
        )

    def scan_resource_conf(self, conf: dict[str, Any]) -> CheckResult:
        defaults = [
            d
            for rule in _blocks(conf, "rule")
            for d in _blocks(rule, "apply_server_side_encryption_by_default")
        ]
        if not defaults:
            self.details.append("no apply_server_side_encryption_by_default block")
            return CheckResult.FAILED
        for d in defaults:
            algorithm = _first(d, "sse_algorithm")
            if algorithm not in KMS_ALGORITHMS:
                self.details.append(f'sse_algorithm is "{algorithm}", expected aws:kms')
                return CheckResult.FAILED
            if not _first(d, "kms_master_key_id"):
                self.details.append("kms_master_key_id is not set (AWS-managed key)")
                return CheckResult.FAILED
        return CheckResult.PASSED

    def get_evaluated_keys(self) -> list[str]:
        return [
            "rule/[0]/apply_server_side_encryption_by_default/[0]/sse_algorithm",
            "rule/[0]/apply_server_side_encryption_by_default/[0]/kms_master_key_id",
        ]


check = S3SseUsesCustomerManagedKms()
