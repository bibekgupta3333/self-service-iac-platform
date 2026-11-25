# Minimal stand-in for a platform module, used only to generate plan fixtures.
# `secure` toggles every control the OPA policies check.

terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.0.0"
    }
  }
}

variable "name" {
  description = "Base name for the bucket and queue."
  type        = string
}

variable "secure" {
  description = "Whether to apply the platform's secure defaults."
  type        = bool
}

variable "force_destroy" {
  description = "Allow destroying a non-empty bucket."
  type        = bool
  default     = false
}

resource "aws_kms_key" "this" {
  description         = "${var.name} data key"
  enable_key_rotation = var.secure
}

resource "aws_s3_bucket" "this" {
  bucket        = "${var.name}-data"
  force_destroy = var.force_destroy
}

resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = var.secure ? "aws:kms" : "AES256"
      kms_master_key_id = var.secure ? aws_kms_key.this.arn : null
    }
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  bucket                  = aws_s3_bucket.this.id
  block_public_acls       = true
  block_public_policy     = var.secure
  ignore_public_acls      = true
  restrict_public_buckets = var.secure
}

resource "aws_sqs_queue" "this" {
  name                    = "${var.name}-events"
  kms_master_key_id       = var.secure ? aws_kms_key.this.arn : null
  sqs_managed_sse_enabled = var.secure ? null : false
}

resource "aws_iam_policy" "consumer" {
  name = "${var.name}-consumer"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "Consume"
      Effect   = "Allow"
      Action   = var.secure ? ["sqs:ReceiveMessage", "sqs:DeleteMessage"] : ["sqs:*"]
      Resource = var.secure ? "arn:aws:sqs:us-east-1:000000000000:${var.name}-events" : "*"
    }]
  })
}

output "bucket" {
  description = "Bucket name."
  value       = aws_s3_bucket.this.bucket
}
