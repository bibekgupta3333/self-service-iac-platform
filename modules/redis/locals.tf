locals {
  module_name    = "redis"
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

  token_auth = var.auth_mode == "token"

  # Two or more nodes give a replica to fail over to (docs/04 section 3.2).
  num_cache_clusters = coalesce(var.num_cache_clusters, local.is_production ? 2 : 1)
  failover           = local.num_cache_clusters >= 2

  # Final snapshot outside development, where TTL destroys must stay cheap.
  # The suffix keeps names unique across re-creations (snapshot.tf).
  final_snapshot_identifier = local.is_development ? null : "${local.full_name}-final-${random_id.final_snapshot[0].hex}"

  log_types      = ["slow-log", "engine-log"]
  log_group_base = "/aws/elasticache/${local.full_name}"
}
