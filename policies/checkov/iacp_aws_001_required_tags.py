"""IACP_AWS_001: taggable resources must carry the mandatory iacp tags.

Modules merge the mandatory tags into ``local.tags`` (docs/04-module-registry.md);
``iacp:stack`` is added by the generated root's provider ``default_tags`` and is
verified on the plan by OPA (policies/opa/tags.rego), so it is not required here.
"""

from __future__ import annotations

from typing import Any

from _conf import first, is_unresolved
from checkov.common.models.enums import CheckCategories, CheckResult
from checkov.terraform.checks.resource.base_resource_check import BaseResourceCheck

REQUIRED_TAGS = ("iacp:team", "iacp:environment", "iacp:managed-by")

TAGGABLE_RESOURCES = (
    "aws_s3_bucket",
    "aws_sqs_queue",
    "aws_lambda_function",
    "aws_kms_key",
    "aws_iam_role",
    "aws_iam_policy",
    "aws_cloudwatch_log_group",
    "aws_cloudwatch_metric_alarm",
    "aws_sns_topic",
    "aws_dynamodb_table",
)


class RequiredIacpTags(BaseResourceCheck):
    def __init__(self) -> None:
        super().__init__(
            name="Ensure taggable resources set the mandatory iacp:* tags",
            id="IACP_AWS_001",
            categories=[CheckCategories.CONVENTION],
            supported_resources=TAGGABLE_RESOURCES,
            guideline="Set tags = local.tags, where local.tags merges var.tags with "
            + ", ".join(REQUIRED_TAGS),
        )

    def scan_resource_conf(self, conf: dict[str, Any]) -> CheckResult:
        tags = first(conf, "tags")
        if tags is None:
            self.details.append("no tags argument; set tags = local.tags")
            return CheckResult.FAILED
        if is_unresolved(tags):
            # Expression Checkov cannot evaluate statically; OPA checks tags_all on the plan.
            return CheckResult.UNKNOWN
        if not isinstance(tags, dict):
            return CheckResult.UNKNOWN
        missing = [t for t in REQUIRED_TAGS if not tags.get(t)]
        if missing:
            self.details.append(f"missing tags: {', '.join(missing)}")
            return CheckResult.FAILED
        return CheckResult.PASSED

    def get_evaluated_keys(self) -> list[str]:
        return ["tags"]


check = RequiredIacpTags()
