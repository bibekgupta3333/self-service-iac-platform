output "id" {
  description = "Bucket name."
  value       = aws_s3_bucket.this.id
}

output "arn" {
  description = "Bucket ARN."
  value       = aws_s3_bucket.this.arn
}

output "name" {
  description = "Computed bucket name (<team>-<environment>-<name>)."
  value       = local.full_name
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting the bucket."
  value       = local.kms_key_arn
}

output "read_policy_json" {
  description = "IAM policy document granting read-only access to the bucket and its key."
  value       = data.aws_iam_policy_document.read.json
}

output "write_policy_json" {
  description = "IAM policy document granting read/write access to the bucket and its key."
  value       = data.aws_iam_policy_document.write.json
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
