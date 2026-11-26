# Least-privilege policy document for principals that publish their own
# notifications to the alarm topic (for example a Lambda function). The module
# creates no roles; callers attach this document to their own principals.
data "aws_iam_policy_document" "publisher" {
  statement {
    sid       = "PublishToAlarmTopic"
    effect    = "Allow"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.this.arn]
  }

  statement {
    sid       = "EncryptThroughSNS"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
    resources = [local.kms_key_arn]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["sns.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
}
