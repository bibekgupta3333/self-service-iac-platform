# Least-privilege policy documents for consumers. The module creates no
# roles; callers attach these documents to their own principals.

data "aws_iam_policy_document" "read" {
  statement {
    sid       = "ListBucket"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.this.arn]
  }

  statement {
    sid       = "ReadObjects"
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:GetObjectVersion"]
    resources = ["${aws_s3_bucket.this.arn}/*"]
  }

  statement {
    sid       = "DecryptObjects"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [local.kms_key_arn]
  }
}

data "aws_iam_policy_document" "write" {
  source_policy_documents = [data.aws_iam_policy_document.read.json]

  statement {
    sid       = "WriteObjects"
    effect    = "Allow"
    actions   = ["s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"]
    resources = ["${aws_s3_bucket.this.arn}/*"]
  }

  statement {
    sid       = "EncryptObjects"
    effect    = "Allow"
    actions   = ["kms:Encrypt", "kms:GenerateDataKey"]
    resources = [local.kms_key_arn]
  }
}
