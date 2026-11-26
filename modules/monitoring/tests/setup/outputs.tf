output "queue_arn" {
  description = "ARN of the subscriber queue."
  value       = module.subscriber.arn
}

output "queue_name" {
  description = "Name of the subscriber queue."
  value       = module.subscriber.name
}
