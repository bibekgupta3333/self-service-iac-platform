resource "aws_sns_topic_subscription" "this" {
  for_each = var.subscriptions

  topic_arn            = aws_sns_topic.this.arn
  protocol             = each.value.protocol
  endpoint             = each.value.endpoint
  raw_message_delivery = each.value.raw_message_delivery
}

# SNS can only invoke a function whose resource policy allows it. The grant is
# additive (one statement per subscription) and limited to this topic and
# account, so a lambda subscription works without hand-written permissions.
resource "aws_lambda_permission" "sns" {
  for_each = { for k, s in var.subscriptions : k => s if s.protocol == "lambda" }

  # Unique per topic and subscription, and within the 100-character limit.
  statement_id   = "iacp-sns-${substr(sha1(local.full_name), 0, 12)}-${each.key}"
  action         = "lambda:InvokeFunction"
  function_name  = each.value.endpoint
  principal      = "sns.amazonaws.com"
  source_arn     = aws_sns_topic.this.arn
  source_account = data.aws_caller_identity.current.account_id
}
