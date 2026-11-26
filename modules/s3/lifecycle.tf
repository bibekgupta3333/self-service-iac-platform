resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  # One bucket-wide rule: clean up failed multipart uploads, expire noncurrent
  # versions and (optionally) move current objects to STANDARD_IA.
  rule {
    id     = "default"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }

    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_version_expiration_days
    }

    dynamic "transition" {
      for_each = var.transition_to_ia_days == null ? [] : [var.transition_to_ia_days]

      content {
        days          = transition.value
        storage_class = "STANDARD_IA"
      }
    }
  }

  # Rules on noncurrent versions need the versioning configuration first.
  depends_on = [aws_s3_bucket_versioning.this]
}
