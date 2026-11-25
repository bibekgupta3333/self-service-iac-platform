resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_version_expiration_days
    }
  }

  dynamic "rule" {
    for_each = var.transition_to_ia_days == null ? [] : [var.transition_to_ia_days]

    content {
      id     = "transition-to-standard-ia"
      status = "Enabled"

      filter {}

      abort_incomplete_multipart_upload {
        days_after_initiation = 7
      }

      transition {
        days          = rule.value
        storage_class = "STANDARD_IA"
      }
    }
  }

  # Rules on noncurrent versions need the versioning configuration first.
  depends_on = [aws_s3_bucket_versioning.this]
}
