resource "aws_s3_bucket" "this" {
  #checkov:skip=CKV_AWS_144:Cross-region replication is a per-stack decision, not a module default.
  #checkov:skip=CKV2_AWS_62:Event notifications are wired by consumers, not by the storage module.
  #checkov:skip=CKV_AWS_18:Access logging is configured through var.access_log_bucket (aws_s3_bucket_logging.this).
  bucket        = local.full_name
  force_destroy = var.force_destroy
  tags          = local.tags
}
