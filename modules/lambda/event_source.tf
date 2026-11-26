resource "aws_lambda_event_source_mapping" "sqs" {
  count = local.has_sqs_trigger ? 1 : 0

  event_source_arn        = var.sqs_trigger.queue_arn
  function_name           = aws_lambda_function.this.arn
  batch_size              = var.sqs_trigger.batch_size
  function_response_types = ["ReportBatchItemFailures"]
  tags                    = local.tags

  # The role needs SQS permissions before Lambda validates the mapping.
  depends_on = [aws_iam_role_policy.this, aws_iam_role_policy.extra]
}
