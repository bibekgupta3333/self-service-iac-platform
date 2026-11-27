output "id" {
  description = "Alarm topic ARN (the SNS topic identifier)."
  value       = aws_sns_topic.this.id
}

output "arn" {
  description = "Alarm topic ARN."
  value       = aws_sns_topic.this.arn
}

output "name" {
  description = "Alarm topic name (<team>-<environment>-<name>)."
  value       = aws_sns_topic.this.name
}

output "sns_topic_arn" {
  description = "Alarm topic ARN, to use as alarm_actions in other modules (for example sqs)."
  value       = aws_sns_topic.this.arn
}

output "dashboard_name" {
  description = "CloudWatch dashboard name, or null when there are no widgets and no alarms."
  value       = local.create_dashboard ? aws_cloudwatch_dashboard.this[0].dashboard_name : null
}

output "dashboard_url" {
  description = "Console URL of the dashboard, or null when there is no dashboard."
  value       = local.create_dashboard ? "https://${data.aws_region.current.name}.console.aws.amazon.com/cloudwatch/home?region=${data.aws_region.current.name}#dashboards:name=${aws_cloudwatch_dashboard.this[0].dashboard_name}" : null
}

output "alarm_arns" {
  description = "ARNs of the alarms, keyed by <kind>-<key>-<metric> for the helper inputs (for example sqs-orders-oldest-message-age) and metric-<name> for metric_alarms."
  value       = { for k, a in aws_cloudwatch_metric_alarm.this : k => a.arn }
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting the topic."
  value       = local.kms_key_arn
}

output "publisher_policy_json" {
  description = "IAM policy document granting sns:Publish on the topic and data-key access through SNS on its key."
  value       = data.aws_iam_policy_document.publisher.json
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
