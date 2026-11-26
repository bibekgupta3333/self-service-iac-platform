resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.id

  # "Disabled" is the creation-time value for an unversioned bucket; "Suspended"
  # would leave a new bucket permanently in the suspended state.
  versioning_configuration {
    status = (var.versioning_enabled || local.is_production) ? "Enabled" : "Disabled"
  }
}
