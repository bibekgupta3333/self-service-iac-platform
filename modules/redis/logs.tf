resource "aws_cloudwatch_log_group" "this" {
  #checkov:skip=CKV_AWS_338:Retention is an input (default 30 days per docs/04); one-year retention is a per-stack compliance choice.
  for_each = toset(local.log_types)

  name              = "${local.log_group_base}/${each.key}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.kms_key_arn
  tags              = local.tags
}
