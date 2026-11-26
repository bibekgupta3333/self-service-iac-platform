locals {
  module_name    = "sqs"
  module_version = "1.0.0"

  full_name     = "${var.team}-${var.environment}-${var.name}"
  suffix        = var.fifo ? ".fifo" : ""
  queue_name    = "${local.full_name}${local.suffix}"
  dlq_name      = "${local.full_name}-dlq${local.suffix}"
  queue_arn     = "arn:${data.aws_partition.current.partition}:sqs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:${local.queue_name}"
  is_production = var.environment == "production"

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn
}
