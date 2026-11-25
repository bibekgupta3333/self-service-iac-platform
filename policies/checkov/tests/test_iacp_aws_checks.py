"""Positive and negative cases for every IACP_AWS_* custom check."""

from __future__ import annotations

TAGS = """
locals {
  tags = merge(var.extra, {
    "iacp:team"        = "payments"
    "iacp:environment" = "development"
    "iacp:managed-by"  = "iacp"
  })
}

variable "extra" {
  type    = map(string)
  default = {}
}
"""


def test_iacp_aws_001_required_tags(scan):
    results = scan(
        "IACP_AWS_001",
        TAGS
        + """
resource "aws_sqs_queue" "tagged" {
  name = "a"
  tags = local.tags
}

resource "aws_sqs_queue" "literal" {
  name = "b"
  tags = { "iacp:team" = "payments", "iacp:environment" = "dev", "iacp:managed-by" = "iacp" }
}

resource "aws_sqs_queue" "untagged" {
  name = "c"
}

variable "dynamic" {
  type = map(string)
}

# Tags taken only from a caller variable (no mandatory merge) fail.
resource "aws_sqs_queue" "caller_only" {
  name = "f"
  tags = var.dynamic
}

resource "aws_s3_bucket" "partial" {
  bucket = "d"
  tags   = { "iacp:team" = "payments" }
}

resource "aws_s3_bucket" "empty_value" {
  bucket = "e"
  tags   = { "iacp:team" = "", "iacp:environment" = "dev", "iacp:managed-by" = "iacp" }
}
""",
    )
    assert results["passed"] == {"aws_sqs_queue.tagged", "aws_sqs_queue.literal"}
    assert results["failed"] == {
        "aws_sqs_queue.untagged",
        "aws_sqs_queue.caller_only",
        "aws_s3_bucket.partial",
        "aws_s3_bucket.empty_value",
    }


def test_iacp_aws_001_ignores_untaggable_resources(scan):
    results = scan(
        "IACP_AWS_001",
        """
resource "aws_s3_bucket_versioning" "v" {
  bucket = "a"
  versioning_configuration { status = "Enabled" }
}
""",
    )
    assert results == {"passed": set(), "failed": set(), "skipped": set()}


def test_iacp_aws_002_s3_sse_kms(scan):
    results = scan(
        "IACP_AWS_002",
        """
resource "aws_kms_key" "k" {
  enable_key_rotation = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "cmk" {
  bucket = "a"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.k.arn
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "dsse" {
  bucket = "b"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms:dsse"
      kms_master_key_id = aws_kms_key.k.arn
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "aes" {
  bucket = "c"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "aws_managed" {
  bucket = "d"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "no_default" {
  bucket = "e"
  rule {
    bucket_key_enabled = true
  }
}
""",
    )
    prefix = "aws_s3_bucket_server_side_encryption_configuration."
    assert results["passed"] == {prefix + "cmk", prefix + "dsse"}
    assert results["failed"] == {prefix + "aes", prefix + "aws_managed", prefix + "no_default"}


def test_iacp_aws_003_lambda_tracing_and_concurrency(scan):
    base = """
  function_name = "{name}"
  role          = "arn:aws:iam::000000000000:role/r"
  handler       = "index.handler"
  runtime       = "python3.12"
  filename      = "f.zip"
"""

    def fn(name: str, extra: str) -> str:
        return f'resource "aws_lambda_function" "{name}" {{{base.format(name=name)}{extra}\n}}\n'

    results = scan(
        "IACP_AWS_003",
        """
variable "concurrency" {
  type = number
}
"""
        + fn("good", 'tracing_config { mode = "Active" }\nreserved_concurrent_executions = 5')
        + fn("from_var", 'tracing_config { mode = "Active" }\nreserved_concurrent_executions = var.concurrency')
        + fn("passthrough", 'tracing_config { mode = "PassThrough" }\nreserved_concurrent_executions = 5')
        + fn("no_tracing", "reserved_concurrent_executions = 5")
        + fn("no_concurrency", 'tracing_config { mode = "Active" }')
        + fn("unreserved", 'tracing_config { mode = "Active" }\nreserved_concurrent_executions = -1'),
    )
    assert results["passed"] == {"aws_lambda_function.good", "aws_lambda_function.from_var"}
    assert results["failed"] == {
        "aws_lambda_function.passthrough",
        "aws_lambda_function.no_tracing",
        "aws_lambda_function.no_concurrency",
        "aws_lambda_function.unreserved",
    }
