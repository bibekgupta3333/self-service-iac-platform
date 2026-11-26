# Queue with every option exercised: custom timings, redrive count and alarm
# notifications to an SNS topic. The topic uses a customer managed key whose
# policy lets CloudWatch publish; the AWS-managed alias/aws/sns key cannot be
# granted to CloudWatch, so alarms would silently fail to notify.

locals {
  platform_tags = {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:managed-by"  = "iacp"
  }
}

data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

data "aws_iam_policy_document" "alarm_topic_key" {
  #checkov:skip=CKV_AWS_109:A key policy's resource "*" means this key only; principals are the account root and CloudWatch for this account.
  #checkov:skip=CKV_AWS_111:A key policy's resource "*" means this key only; principals are the account root and CloudWatch for this account.
  #checkov:skip=CKV_AWS_356:A key policy's resource "*" means this key only; principals are the account root and CloudWatch for this account.
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
    sid       = "CloudWatchAlarmsPublish"
    effect    = "Allow"
    actions   = ["kms:GenerateDataKey*", "kms:Decrypt"]
    resources = ["*"]

    principals {
      type        = "Service"
      identifiers = ["cloudwatch.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_kms_key" "alarms" {
  description             = "Encrypts the ${var.name} alarm topic"
  enable_key_rotation     = true
  deletion_window_in_days = 7
  policy                  = data.aws_iam_policy_document.alarm_topic_key.json
  tags                    = local.platform_tags
}

resource "aws_sns_topic" "alarms" {
  name              = "${var.team}-${var.environment}-${var.name}-alarms"
  kms_master_key_id = aws_kms_key.alarms.arn
  tags              = local.platform_tags
}

module "queue" {
  source = "../../"

  name                             = var.name
  team                             = var.team
  environment                      = var.environment
  stack                            = var.stack
  visibility_timeout_seconds       = 60
  message_retention_seconds        = 86400
  max_receive_count                = 3
  dlq_retention_seconds            = 1209600
  alarm_actions                    = [aws_sns_topic.alarms.arn]
  oldest_message_age_alarm_seconds = 900
  key_service_principals           = ["sns.amazonaws.com"]

  tags = {
    "cost-center" = "cc-1234"
  }
}
