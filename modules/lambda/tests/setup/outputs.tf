output "artifact_s3_uri" {
  description = "s3:// URI of the handler package."
  value       = "s3://${aws_s3_object.handler.bucket}/${aws_s3_object.handler.key}"
}

output "source_code_hash" {
  description = "Base64 SHA-256 of the handler package."
  value       = data.archive_file.handler.output_base64sha256
}

output "queue_arn" {
  description = "ARN of the trigger queue."
  value       = module.trigger.arn
}

output "queue_kms_key_arn" {
  description = "KMS key encrypting the trigger queue."
  value       = module.trigger.kms_key_arn
}

output "consumer_policy_json" {
  description = "Consumer policy for the trigger queue."
  value       = module.trigger.consumer_policy_json
}
