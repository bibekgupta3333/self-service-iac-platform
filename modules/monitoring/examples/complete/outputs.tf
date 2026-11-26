output "sns_topic_arn" {
  description = "ARN of the alarm topic."
  value       = module.monitoring.sns_topic_arn
}

output "kms_key_arn" {
  description = "KMS key encrypting the alarm topic."
  value       = module.monitoring.kms_key_arn
}

output "dashboard_name" {
  description = "Name of the dashboard."
  value       = module.monitoring.dashboard_name
}

output "alarm_arns" {
  description = "Alarms created by the monitoring module."
  value       = module.monitoring.alarm_arns
}

output "queue_name" {
  description = "Name of the monitored queue."
  value       = module.queue.name
}
