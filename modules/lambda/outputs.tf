output "id" {
  description = "Function name."
  value       = aws_lambda_function.this.function_name
}

output "arn" {
  description = "Function ARN."
  value       = aws_lambda_function.this.arn
}

output "name" {
  description = "Computed function name (<team>-<environment>-<name>)."
  value       = local.full_name
}

output "role_arn" {
  description = "ARN of the function's execution role."
  value       = aws_iam_role.this.arn
}

output "invoke_arn" {
  description = "ARN used by API Gateway and other services to invoke the function."
  value       = aws_lambda_function.this.invoke_arn
}

output "log_group_name" {
  description = "Name of the function's CloudWatch log group."
  value       = aws_cloudwatch_log_group.this.name
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting environment variables and logs."
  value       = local.kms_key_arn
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
