output "id" {
  description = "ECS service ID (its ARN)."
  value       = aws_ecs_service.this.id
}

output "arn" {
  description = "ECS service ARN."
  value       = aws_ecs_service.this.id
}

output "name" {
  description = "Computed name (<team>-<environment>-<name>) of the service, repository and task family."
  value       = local.full_name
}

output "service_name" {
  description = "ECS service name."
  value       = aws_ecs_service.this.name
}

output "cluster_arn" {
  description = "ARN of the cluster running the service (module-created or cluster_arn)."
  value       = local.cluster_arn
}

output "alb_dns_name" {
  description = "DNS name of the load balancer, or null without one."
  value       = local.has_load_balancer ? aws_lb.this[0].dns_name : null
}

output "service_url" {
  description = "HTTPS URL of the load balancer, or null without one. Point a DNS record matching the certificate at alb_dns_name."
  value       = local.has_load_balancer ? "https://${aws_lb.this[0].dns_name}" : null
}

output "ecr_repository_url" {
  description = "URL of the service's ECR repository; CI pushes <url>:<image_tag>."
  value       = aws_ecr_repository.this.repository_url
}

output "task_role_arn" {
  description = "ARN of the task role the application runs as."
  value       = aws_iam_role.task.arn
}

output "execution_role_arn" {
  description = "ARN of the execution role ECS uses to start tasks."
  value       = aws_iam_role.execution.arn
}

output "security_group_id" {
  description = "Security group of the tasks. Pass it to the postgres or redis module's allowed_security_group_ids."
  value       = aws_security_group.service.id
}

output "log_group_name" {
  description = "CloudWatch log group of the container."
  value       = aws_cloudwatch_log_group.this.name
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting the ECR repository and logs."
  value       = local.kms_key_arn
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
