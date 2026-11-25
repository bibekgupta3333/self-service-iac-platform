output "bucket_id" {
  description = "Name of the example bucket."
  value       = module.bucket.id
}

output "bucket_arn" {
  description = "ARN of the example bucket."
  value       = module.bucket.arn
}

output "kms_key_arn" {
  description = "KMS key encrypting the example bucket."
  value       = module.bucket.kms_key_arn
}

output "access_log_bucket_id" {
  description = "Name of the bucket receiving access logs."
  value       = module.access_logs.id
}

output "read_policy_json" {
  description = "Read-only IAM policy document for the example bucket."
  value       = module.bucket.read_policy_json
}
