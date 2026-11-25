resource "aws_kms_key" "this" {
  count = local.create_kms_key ? 1 : 0

  description             = "Encrypts SQS queues ${local.queue_name} and ${local.dlq_name}"
  enable_key_rotation     = true
  deletion_window_in_days = local.is_production ? 30 : 7
  policy                  = data.aws_iam_policy_document.kms[0].json
  tags                    = local.tags
}

resource "aws_kms_alias" "this" {
  count = local.create_kms_key ? 1 : 0

  name          = "alias/iacp/sqs/${local.full_name}"
  target_key_id = aws_kms_key.this[0].key_id
}

# Key administration stays with the owning account. Producers and consumers
# get key access through the producer/consumer policy documents.
data "aws_iam_policy_document" "kms" {
  count = local.create_kms_key ? 1 : 0

  #checkov:skip=CKV_AWS_109:A key policy's resource "*" means this key only; the principal is the owning account root.
  #checkov:skip=CKV_AWS_111:A key policy's resource "*" means this key only; the principal is the owning account root.
  #checkov:skip=CKV_AWS_356:A key policy's resource "*" means this key only; the principal is the owning account root.
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
}
