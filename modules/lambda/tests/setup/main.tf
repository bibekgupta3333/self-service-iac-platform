# Test fixture for modules/lambda: an artifact bucket holding a tiny Python
# handler and an SQS queue to use as a trigger. Built from the platform's own
# s3 and sqs modules so the fixture keeps the same secure defaults.

terraform {
  required_version = ">= 1.10, < 2.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 6.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = ">= 2.4, < 3.0"
    }
  }
}

variable "suffix" {
  description = "Suffix that keeps fixture names unique."
  type        = string
  default     = "tftest"
}

module "artifacts" {
  source = "../../../s3"

  name          = "lambda-artifacts-${var.suffix}"
  team          = "platform"
  environment   = "development"
  stack         = "tftest"
  force_destroy = true
}

module "trigger" {
  source = "../../../sqs"

  name        = "lambda-trigger-${var.suffix}"
  team        = "platform"
  environment = "development"
  stack       = "tftest"

  # Lambda requires the queue visibility timeout to cover the function timeout.
  visibility_timeout_seconds = 60
}

data "archive_file" "handler" {
  type        = "zip"
  output_path = "${path.module}/handler.zip"

  source {
    filename = "app.py"
    content  = <<-PY
      def handler(event, context):
          return {"ok": True, "records": len(event.get("Records", []))}
    PY
  }
}

resource "aws_s3_object" "handler" {
  bucket      = module.artifacts.id
  key         = "handlers/app.zip"
  source      = data.archive_file.handler.output_path
  source_hash = data.archive_file.handler.output_base64sha256
  kms_key_id  = module.artifacts.kms_key_arn
}

output "artifact_s3_uri" {
  description = "s3:// URI of the handler package."
  value       = "s3://${aws_s3_object.handler.bucket}/${aws_s3_object.handler.key}"
}

output "source_code_hash" {
  description = "Base64 SHA-256 of the handler package."
  value       = data.archive_file.handler.output_base64sha256
}

output "queue_arn" {
  description = "ARN of the trigger queue."
  value       = module.trigger.arn
}

output "consumer_policy_json" {
  description = "Consumer policy for the trigger queue."
  value       = module.trigger.consumer_policy_json
}
