locals {
  module_name    = "s3"
  module_version = "1.0.0"

  full_name     = "${var.team}-${var.environment}-${var.name}"
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
