output "queue_url" {
  description = "URL of the example queue."
  value       = module.queue.id
}

output "queue_arn" {
  description = "ARN of the example queue."
  value       = module.queue.arn
}

output "queue_name" {
  description = "Name of the example queue."
  value       = module.queue.name
}

output "dlq_url" {
  description = "URL of the dead-letter queue."
  value       = module.queue.dlq_url
}

output "dlq_arn" {
  description = "ARN of the dead-letter queue."
  value       = module.queue.dlq_arn
}

output "kms_key_arn" {
  description = "KMS key encrypting both queues."
  value       = module.queue.kms_key_arn
}

output "consumer_policy_json" {
  description = "Consumer IAM policy document."
  value       = module.queue.consumer_policy_json
}
