output "service_name" {
  description = "ECS service name."
  value       = module.api.service_name
}

output "cluster_arn" {
  description = "Cluster running the service."
  value       = module.api.cluster_arn
}

output "alb_dns_name" {
  description = "Load balancer DNS name."
  value       = module.api.alb_dns_name
}

output "ecr_repository_url" {
  description = "Repository CI pushes the image to."
  value       = module.api.ecr_repository_url
}

output "database_endpoint" {
  description = "Database host name."
  value       = module.database.endpoint
}
