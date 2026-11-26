output "id" {
  description = "Replication group ID."
  value       = aws_elasticache_replication_group.this.id
}

output "arn" {
  description = "Replication group ARN."
  value       = aws_elasticache_replication_group.this.arn
}

output "name" {
  description = "Computed replication group ID (<team>-<environment>-<name>)."
  value       = local.full_name
}

output "primary_endpoint" {
  description = "DNS name of the primary node (reads and writes). Connect with TLS."
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "reader_endpoint" {
  description = "DNS name that balances reads across replicas. Connect with TLS."
  value       = aws_elasticache_replication_group.this.reader_endpoint_address
}

output "port" {
  description = "TCP port the cache listens on."
  value       = aws_elasticache_replication_group.this.port
}

output "secret_arn" {
  description = "ARN of the Secrets Manager secret holding the AUTH token (auth_mode token), or null with RBAC. The token itself is never an output."
  value       = local.token_auth ? aws_secretsmanager_secret.auth_token[0].arn : null
}

output "connection" {
  description = "Connection details for consumers: host, port and the AUTH token secret ARN (never a plaintext token)."
  value = {
    host       = aws_elasticache_replication_group.this.primary_endpoint_address
    port       = aws_elasticache_replication_group.this.port
    secret_arn = local.token_auth ? aws_secretsmanager_secret.auth_token[0].arn : null
  }
}

output "security_group_id" {
  description = "Security group attached to the cache."
  value       = aws_security_group.this.id
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting data at rest, logs and the AUTH token secret."
  value       = local.kms_key_arn
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
