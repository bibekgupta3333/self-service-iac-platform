output "endpoint" {
  description = "Database host name."
  value       = module.database.endpoint
}

output "port" {
  description = "Database port."
  value       = module.database.port
}

output "secret_arn" {
  description = "RDS-managed master user secret."
  value       = module.database.secret_arn
}

output "security_group_id" {
  description = "Security group of the database."
  value       = module.database.security_group_id
}
