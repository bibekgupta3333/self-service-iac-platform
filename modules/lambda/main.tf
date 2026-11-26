resource "aws_lambda_function" "this" {
  #checkov:skip=CKV_AWS_117:VPC placement arrives with the vpc module (post-MVP); functions reach AWS APIs over public endpoints with IAM auth.
  #checkov:skip=CKV_AWS_272:Code signing for production is planned for a later minor release (docs/04 section 10); 1.0.0 does not configure it.
  #checkov:skip=CKV_AWS_116:A dead-letter target is configured through var.dead_letter_target_arn; SQS-triggered functions use the queue's DLQ.
  function_name                  = local.full_name
  description                    = "Managed by iacp (stack ${var.stack})"
  role                           = aws_iam_role.this.arn
  runtime                        = var.runtime
  handler                        = var.handler
  architectures                  = [var.architecture]
  s3_bucket                      = local.artifact_bucket
  s3_key                         = local.artifact_key
  source_code_hash               = var.source_code_hash
  memory_size                    = var.memory_mb
  timeout                        = var.timeout_seconds
  reserved_concurrent_executions = var.reserved_concurrent_executions
  kms_key_arn                    = local.kms_key_arn

  tracing_config {
    mode = var.tracing_mode
  }

  dynamic "environment" {
    for_each = length(var.environment_variables) > 0 ? [var.environment_variables] : []

    content {
      variables = environment.value
    }
  }

  dynamic "dead_letter_config" {
    for_each = local.has_dlq ? [var.dead_letter_target_arn] : []

    content {
      target_arn = dead_letter_config.value
    }
  }

  tags = local.tags

  # The log group must exist (with retention and KMS) before the function can
  # create it implicitly, and the role needs its permissions first.
  depends_on = [
    aws_cloudwatch_log_group.this,
    aws_iam_role_policy.this,
  ]
}
