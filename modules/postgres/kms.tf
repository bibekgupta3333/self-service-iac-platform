resource "aws_kms_key" "this" {
  count = local.create_kms_key ? 1 : 0

  description             = "Encrypts storage, Performance Insights, logs and the master secret of PostgreSQL ${local.full_name}"
  enable_key_rotation     = true
  deletion_window_in_days = local.is_production ? 30 : 7
  policy                  = data.aws_iam_policy_document.kms[0].json
  tags                    = local.tags
}

resource "aws_kms_alias" "this" {
  count = local.create_kms_key ? 1 : 0

  name          = "alias/iacp/postgres/${local.full_name}"
  target_key_id = aws_kms_key.this[0].key_id
}

# RDS and Secrets Manager use the key through grants made on behalf of the
# account (kms:ViaService), which the account-root statement delegates to IAM.
# CloudWatch Logs needs its own grant, limited to this instance's log groups.
data "aws_iam_policy_document" "kms" {
  count = local.create_kms_key ? 1 : 0

  #checkov:skip=CKV_AWS_109:A key policy's resource "*" means this key only; principals are the owning account and the scoped Logs service.
  #checkov:skip=CKV_AWS_111:A key policy's resource "*" means this key only; principals are the owning account and the scoped Logs service.
  #checkov:skip=CKV_AWS_356:A key policy's resource "*" means this key only; principals are the owning account and the scoped Logs service.
  statement {
    sid       = "AccountAdministration"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  statement {
    sid       = "CloudWatchLogsForInstanceLogGroups"
    effect    = "Allow"
    actions   = ["kms:Encrypt*", "kms:Decrypt*", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]

    principals {
      type        = "Service"
      identifiers = ["logs.${data.aws_region.current.name}.amazonaws.com"]
    }

    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:${data.aws_partition.current.partition}:logs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:log-group:${local.log_group_base}/*"]
    }
  }
}
