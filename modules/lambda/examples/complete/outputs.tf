output "function_name" {
  description = "Name of the example function."
  value       = module.function.id
}

output "function_arn" {
  description = "ARN of the example function."
  value       = module.function.arn
}

output "role_arn" {
  description = "Execution role ARN."
  value       = module.function.role_arn
}

output "log_group_name" {
  description = "Function log group."
  value       = module.function.log_group_name
}

output "queue_url" {
  description = "URL of the trigger queue."
  value       = module.queue.id
}
