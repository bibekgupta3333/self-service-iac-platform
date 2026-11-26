output "primary_endpoint" {
  description = "Primary endpoint host name (TLS)."
  value       = module.cache.primary_endpoint
}

output "port" {
  description = "Cache port."
  value       = module.cache.port
}

output "secret_arn" {
  description = "Secret holding the AUTH token."
  value       = module.cache.secret_arn
}

output "security_group_id" {
  description = "Security group of the cache."
  value       = module.cache.security_group_id
}
