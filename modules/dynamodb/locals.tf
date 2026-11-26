locals {
  module_name    = "dynamodb"
  module_version = "1.0.0"

  full_name      = "${var.team}-${var.environment}-${var.name}"
  is_production  = var.environment == "production"
  is_provisioned = var.billing_mode == "PROVISIONED"

  # Deletion protection is on outside development unless the caller decides
  # otherwise; variables.tf rejects turning it off outside development.
  deletion_protection_enabled = coalesce(var.deletion_protection_enabled, var.environment != "development")

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  # Attribute definitions: every key attribute of the table and its indexes,
  # grouped by name (variables.tf guarantees one type per name).
  key_attributes = concat(
    [{ name = var.hash_key, type = var.hash_key_type }],
    var.range_key == null ? [] : [{ name = var.range_key, type = var.range_key_type }],
    flatten([for g in var.global_secondary_indexes : concat(
      [{ name = g.hash_key, type = g.hash_key_type }],
      g.range_key == null ? [] : [{ name = g.range_key, type = g.range_key_type }],
    )]),
  )
  attributes = { for name, types in { for a in local.key_attributes : a.name => a.type... } : name => types[0] }

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn
}
