output "table_name" {
  description = "Name of the example table."
  value       = module.table.name
}

output "table_arn" {
  description = "ARN of the example table."
  value       = module.table.arn
}

output "stream_arn" {
  description = "ARN of the table's stream."
  value       = module.table.stream_arn
}

output "kms_key_arn" {
  description = "KMS key encrypting the table."
  value       = module.table.kms_key_arn
}

output "read_policy_json" {
  description = "Reader IAM policy document."
  value       = module.table.read_policy_json
}
