# A queue-driven function wired the way the platform wires stacks: the s3
# module stores the package, the sqs module provides the trigger and its
# consumer policy, and the lambda module consumes both outputs.

module "artifacts" {
  source = "../../../s3"

  name          = "${var.name}-artifacts"
  team          = var.team
  environment   = var.environment
  stack         = var.stack
  force_destroy = true
}

module "queue" {
  source = "../../../sqs"

  name        = "${var.name}-jobs"
  team        = var.team
  environment = var.environment
  stack       = var.stack

  # Lambda requires the visibility timeout to cover the function timeout.
  visibility_timeout_seconds = 60
}

# A tiny inline Python handler, zipped at plan time.
data "archive_file" "handler" {
  type        = "zip"
  output_path = "${path.module}/handler.zip"

  source {
    filename = "app.py"
    content  = <<-PY
      import json


      def handler(event, context):
          records = event.get("Records", [])
          print(json.dumps({"received": len(records)}))
          return {"batchItemFailures": []}
    PY
  }
}

resource "aws_s3_object" "handler" {
  bucket      = module.artifacts.id
  key         = "${var.name}/app.zip"
  source      = data.archive_file.handler.output_path
  source_hash = data.archive_file.handler.output_base64sha256
  kms_key_id  = module.artifacts.kms_key_arn
}

module "function" {
  source = "../../"

  name             = var.name
  team             = var.team
  environment      = var.environment
  stack            = var.stack
  runtime          = "python3.12"
  handler          = "app.handler"
  artifact_s3_uri  = "s3://${aws_s3_object.handler.bucket}/${aws_s3_object.handler.key}"
  source_code_hash = data.archive_file.handler.output_base64sha256
  memory_mb        = 256
  timeout_seconds  = 30

  reserved_concurrent_executions = 5
  log_retention_days             = 14

  environment_variables = {
    LOG_LEVEL = "info"
  }

  sqs_trigger = {
    queue_arn  = module.queue.arn
    batch_size = 5
  }
  extra_policy_jsons = [module.queue.consumer_policy_json]

  tags = {
    "cost-center" = "cc-1234"
  }
}
