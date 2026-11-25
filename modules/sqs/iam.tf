# Least-privilege policy documents for consumers and producers. The module
# creates no roles; callers attach these documents to their own principals.

data "aws_iam_policy_document" "consumer" {
  statement {
    sid    = "ConsumeMessages"
    effect = "Allow"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
    ]
    resources = [aws_sqs_queue.this.arn]
  }

  statement {
    sid       = "DecryptMessages"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [local.kms_key_arn]
  }
}

data "aws_iam_policy_document" "producer" {
  statement {
    sid       = "SendMessages"
    effect    = "Allow"
    actions   = ["sqs:SendMessage", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"]
    resources = [aws_sqs_queue.this.arn]
  }

  statement {
    sid       = "EncryptMessages"
    effect    = "Allow"
    actions   = ["kms:GenerateDataKey", "kms:Decrypt"]
    resources = [local.kms_key_arn]
  }
}
