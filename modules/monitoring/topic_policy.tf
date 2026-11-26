# Only CloudWatch (and publisher_service_principals) may publish as a service,
# and only on behalf of this account. IAM principals in the account publish
# through identity policies such as publisher_policy_json. Every request must
# use TLS.
resource "aws_sns_topic_policy" "this" {
  arn    = aws_sns_topic.this.arn
  policy = data.aws_iam_policy_document.topic.json
}

data "aws_iam_policy_document" "topic" {
  statement {
    sid       = "ServicesPublishForThisAccount"
    effect    = "Allow"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.this.arn]

    principals {
      type        = "Service"
      identifiers = local.publisher_services
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }

  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.this.arn]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}
