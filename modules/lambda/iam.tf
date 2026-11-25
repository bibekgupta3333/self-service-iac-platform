# Execution role with only what the function needs: write its own logs, send
# traces, decrypt with its key, and (optionally) consume its SQS trigger and
# publish to its dead-letter target. Extra access comes from
# var.extra_policy_jsons, produced by other modules.

resource "aws_iam_role" "this" {
  name                 = local.full_name
  assume_role_policy   = data.aws_iam_policy_document.assume.json
  permissions_boundary = var.permissions_boundary_arn
  tags                 = local.tags
}

data "aws_iam_policy_document" "assume" {
  statement {
    sid     = "LambdaAssumeRole"
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_iam_role_policy" "this" {
  name   = "execution"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.execution.json
}

data "aws_iam_policy_document" "execution" {
  #checkov:skip=CKV_AWS_356:Only the X-Ray statement uses resource "*"; the trace APIs do not support resource-level permissions.
  statement {
    sid       = "WriteOwnLogs"
    effect    = "Allow"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }

  statement {
    sid       = "DecryptWithFunctionKey"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [local.kms_key_arn]
  }

  dynamic "statement" {
    for_each = var.tracing_mode == "Active" ? [1] : []

    content {
      sid       = "WriteTraces"
      effect    = "Allow"
      actions   = ["xray:PutTraceSegments", "xray:PutTelemetryRecords"]
      resources = ["*"]
    }
  }

  dynamic "statement" {
    for_each = local.has_sqs_trigger ? [var.sqs_trigger.queue_arn] : []

    content {
      sid       = "ConsumeTriggerQueue"
      effect    = "Allow"
      actions   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes"]
      resources = [statement.value]
    }
  }

  dynamic "statement" {
    for_each = local.has_dlq ? [var.dead_letter_target_arn] : []

    content {
      sid       = "PublishFailedInvocations"
      effect    = "Allow"
      actions   = [local.dlq_action]
      resources = [statement.value]
    }
  }
}

resource "aws_iam_role_policy" "extra" {
  count = length(var.extra_policy_jsons)

  name   = "extra-${count.index}"
  role   = aws_iam_role.this.id
  policy = var.extra_policy_jsons[count.index]
}
