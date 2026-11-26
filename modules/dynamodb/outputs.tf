output "id" {
  description = "Table name (the DynamoDB table identifier)."
  value       = aws_dynamodb_table.this.id
}

output "arn" {
  description = "Table ARN."
  value       = aws_dynamodb_table.this.arn
}

output "name" {
  description = "Table name (<team>-<environment>-<name>)."
  value       = aws_dynamodb_table.this.name
}

output "stream_arn" {
  description = "ARN of the table's stream, or null when stream_view_type is null."
  value       = var.stream_view_type == null ? null : aws_dynamodb_table.this.stream_arn
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting the table."
  value       = local.kms_key_arn
}

output "read_policy_json" {
  description = "IAM policy document granting read access to the table and its indexes (and its stream, when enabled), and decrypt through DynamoDB on its key."
  value       = data.aws_iam_policy_document.read.json
}

output "write_policy_json" {
  description = "IAM policy document granting item writes on the table, and encrypt and decrypt through DynamoDB on its key."
  value       = data.aws_iam_policy_document.write.json
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
