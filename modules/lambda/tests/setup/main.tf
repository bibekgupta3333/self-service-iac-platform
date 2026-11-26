# Test fixture for modules/lambda: an artifact bucket holding a tiny Python
# handler and an SQS queue to use as a trigger. Built from the platform's own
# s3 and sqs modules so the fixture keeps the same secure defaults.

locals {
  platform_tags = {
    "iacp:team"        = "platform"
    "iacp:environment" = "development"
    "iacp:stack"       = "tftest"
    "iacp:managed-by"  = "iacp"
  }
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
  tags        = local.platform_tags
}
