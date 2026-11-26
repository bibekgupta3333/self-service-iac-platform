locals {
  module_name    = "postgres"
  module_version = "1.0.0"

  full_name      = "${var.team}-${var.environment}-${var.name}"
  is_production  = var.environment == "production"
  is_development = var.environment == "development"

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

  engine_major     = split(".", var.engine_version)[0]
  parameter_family = "postgres${local.engine_major}"

  # Environment-driven protection (docs/04 section 3.2). Development stays
  # disposable so TTL destroys work; every other environment is protected.
  multi_az              = coalesce(var.multi_az, local.is_production)
  backup_retention_days = coalesce(var.backup_retention_days, local.is_production ? 35 : 7)
  deletion_protection   = !local.is_development
  skip_final_snapshot   = local.is_development

  log_exports    = ["postgresql", "upgrade"]
  log_group_base = "/aws/rds/instance/${local.full_name}"
}
