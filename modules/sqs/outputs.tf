output "id" {
  description = "Main queue URL."
  value       = aws_sqs_queue.this.url
}

output "arn" {
  description = "Main queue ARN."
  value       = aws_sqs_queue.this.arn
}

output "name" {
  description = "Main queue name (<team>-<environment>-<name>, plus .fifo for FIFO queues)."
  value       = aws_sqs_queue.this.name
}

output "dlq_arn" {
  description = "Dead-letter queue ARN."
  value       = aws_sqs_queue.dlq.arn
}

output "dlq_url" {
  description = "Dead-letter queue URL."
  value       = aws_sqs_queue.dlq.url
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting both queues."
  value       = local.kms_key_arn
}

output "consumer_policy_json" {
  description = "IAM policy document granting receive/delete access to the main queue and decrypt on its key."
  value       = data.aws_iam_policy_document.consumer.json
}

output "producer_policy_json" {
  description = "IAM policy document granting send access to the main queue and data-key access on its key."
  value       = data.aws_iam_policy_document.producer.json
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
