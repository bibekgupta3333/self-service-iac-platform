resource "aws_elasticache_parameter_group" "this" {
  name        = "${local.full_name}-redis7"
  family      = "redis7"
  description = "Redis ${local.full_name}"
  tags        = local.tags

  parameter {
    name  = "maxmemory-policy"
    value = var.maxmemory_policy
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_elasticache_replication_group" "this" {
  #checkov:skip=CKV2_AWS_50:Multi-AZ automatic failover is on whenever there are 2+ nodes and is required in production (validated, asserted in tests/redis.tftest.hcl); single-node caches are a development/staging cost choice.
  replication_group_id = local.full_name
  description          = "Managed by iacp (stack ${var.stack})"
  engine               = "redis"
  engine_version       = var.engine_version
  parameter_group_name = aws_elasticache_parameter_group.this.name
  node_type            = var.node_type
  port                 = var.port
  num_cache_clusters   = local.num_cache_clusters

  # High availability: a replica in another AZ takes over automatically.
  automatic_failover_enabled = local.failover
  multi_az_enabled           = local.failover

  # Network: private subnets, reachable only through the module group.
  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.this.id]

  # Encryption at rest with a customer-managed key, and TLS required in transit.
  at_rest_encryption_enabled = true
  kms_key_id                 = local.kms_key_arn
  transit_encryption_enabled = true
  transit_encryption_mode    = "required"

  # Authentication: an AUTH token from Secrets Manager, or RBAC user groups.
  auth_token     = local.token_auth ? random_password.auth_token[0].result : null
  user_group_ids = local.token_auth ? null : toset(var.user_group_ids)

  # Recovery.
  snapshot_retention_limit  = var.snapshot_retention_days
  final_snapshot_identifier = local.final_snapshot_identifier

  # Maintenance.
  auto_minor_version_upgrade = true
  apply_immediately          = false

  dynamic "log_delivery_configuration" {
    for_each = aws_cloudwatch_log_group.this

    content {
      destination      = log_delivery_configuration.value.name
      destination_type = "cloudwatch-logs"
      log_format       = "json"
      log_type         = log_delivery_configuration.key
    }
  }

  tags = local.tags
}
