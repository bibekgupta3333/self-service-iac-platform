locals {
  module_name    = "lambda"
  module_version = "1.0.0"

  full_name      = "${var.team}-${var.environment}-${var.name}"
  is_production  = var.environment == "production"
  log_group_name = "/aws/lambda/${local.full_name}"

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  artifact_parts  = regex("^s3://([^/]+)/(.+)$", var.artifact_s3_uri)
  artifact_bucket = local.artifact_parts[0]
  artifact_key    = local.artifact_parts[1]

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn

  has_sqs_trigger = var.sqs_trigger != null
  has_dlq         = var.dead_letter_target_arn != null
  dlq_action      = local.has_dlq ? (strcontains(var.dead_letter_target_arn, ":sqs:") ? "sqs:SendMessage" : "sns:Publish") : null
}
