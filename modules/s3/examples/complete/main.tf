# Bucket with every option exercised: lifecycle transition, access logging to
# a second bucket created with the same module, a service grant on that log
# bucket, and extra user tags.
#
# Note for real AWS: S3 server access logging cannot deliver to a bucket whose
# default encryption is SSE-KMS. On LocalStack this works; in an account, point
# access_logging at the central SSE-S3 log bucket instead.

module "access_logs" {
  source = "../../"

  name          = "${var.name}-logs"
  team          = var.team
  environment   = var.environment
  stack         = var.stack
  force_destroy = true

  key_service_principals = ["logging.s3.amazonaws.com"]

  additional_policy_statements = [{
    sid                   = "AllowServerAccessLogDelivery"
    effect                = "Allow"
    actions               = ["s3:PutObject"]
    principal_type        = "Service"
    principal_identifiers = ["logging.s3.amazonaws.com"]
    conditions = [{
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }]
  }]
}

data "aws_caller_identity" "current" {}

module "bucket" {
  source = "../../"

  name                               = var.name
  team                               = var.team
  environment                        = var.environment
  stack                              = var.stack
  versioning_enabled                 = true
  noncurrent_version_expiration_days = 30
  transition_to_ia_days              = 90
  force_destroy                      = true

  access_logging = {
    target_bucket = module.access_logs.id
  }

  tags = {
    "cost-center" = "cc-1234"
    "purpose"     = "example"
  }
}
