output "id" {
  description = "DB instance identifier."
  value       = aws_db_instance.this.identifier
}

output "arn" {
  description = "DB instance ARN."
  value       = aws_db_instance.this.arn
}

output "name" {
  description = "Computed instance identifier (<team>-<environment>-<name>)."
  value       = local.full_name
}

output "endpoint" {
  description = "DNS name clients connect to (without the port)."
  value       = aws_db_instance.this.address
}

output "port" {
  description = "TCP port the database listens on."
  value       = aws_db_instance.this.port
}

output "secret_arn" {
  description = "ARN of the Secrets Manager secret RDS manages for the master user. The password itself is never an output."
  value       = aws_db_instance.this.master_user_secret[0].secret_arn
}

output "connection" {
  description = "Connection details for consumers: host, port and the master secret ARN (never a plaintext password)."
  value = {
    host       = aws_db_instance.this.address
    port       = aws_db_instance.this.port
    secret_arn = aws_db_instance.this.master_user_secret[0].secret_arn
  }
}

output "security_group_id" {
  description = "Security group attached to the instance."
  value       = aws_security_group.this.id
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting storage, Performance Insights, logs and the master secret."
  value       = local.kms_key_arn
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
