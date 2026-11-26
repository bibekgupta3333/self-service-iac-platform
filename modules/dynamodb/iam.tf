# Least-privilege policy documents for readers and writers. The module creates
# no roles; callers attach these documents to their own principals. Actions are
# explicit (no wildcards), so the documents pass the lambda module's
# extra_policy_jsons guard. Key use is limited to calls made through DynamoDB.

data "aws_iam_policy_document" "read" {
  statement {
    sid    = "ReadItems"
    effect = "Allow"
    actions = [
      "dynamodb:BatchGetItem",
      "dynamodb:ConditionCheckItem",
      "dynamodb:DescribeTable",
      "dynamodb:GetItem",
      "dynamodb:Query",
      "dynamodb:Scan",
    ]
    resources = [aws_dynamodb_table.this.arn, "${aws_dynamodb_table.this.arn}/index/*"]
  }

  # Stream consumers (for example a Lambda event source mapping) read the
  # table's stream; only present when stream_view_type enables one.
  dynamic "statement" {
    for_each = var.stream_view_type == null ? [] : [aws_dynamodb_table.this.stream_arn]

    content {
      sid    = "ReadStream"
      effect = "Allow"
      actions = [
        "dynamodb:DescribeStream",
        "dynamodb:GetRecords",
        "dynamodb:GetShardIterator",
      ]
      resources = [statement.value]
    }
  }

  dynamic "statement" {
    for_each = var.stream_view_type == null ? [] : [true]

    content {
      # ListStreams does not support resource-level permissions.
      sid       = "ListStreams"
      effect    = "Allow"
      actions   = ["dynamodb:ListStreams"]
      resources = ["*"]
    }
  }

  statement {
    sid       = "DecryptThroughDynamoDB"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [local.kms_key_arn]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["dynamodb.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "write" {
  statement {
    sid    = "WriteItems"
    effect = "Allow"
    actions = [
      "dynamodb:BatchWriteItem",
      "dynamodb:ConditionCheckItem",
      "dynamodb:DeleteItem",
      "dynamodb:DescribeTable",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
    ]
    resources = [aws_dynamodb_table.this.arn]
  }

  statement {
    sid       = "EncryptThroughDynamoDB"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:DescribeKey", "kms:Encrypt", "kms:GenerateDataKey"]
    resources = [local.kms_key_arn]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["dynamodb.${data.aws_region.current.name}.amazonaws.com"]
    }
  }
}
