resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  alarm_name          = "${local.dlq_name}-not-empty"
  alarm_description   = "Messages are landing in the dead-letter queue ${local.dlq_name}."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = aws_sqs_queue.dlq.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_actions
  ok_actions          = var.alarm_actions
  tags                = local.tags
}

resource "aws_cloudwatch_metric_alarm" "oldest_message_age" {
  alarm_name          = "${local.queue_name}-oldest-message-age"
  alarm_description   = "The oldest message in ${local.queue_name} is older than ${var.oldest_message_age_alarm_seconds}s."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateAgeOfOldestMessage"
  dimensions          = { QueueName = aws_sqs_queue.this.name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = var.oldest_message_age_alarm_seconds
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_actions
  ok_actions          = var.alarm_actions
  tags                = local.tags
}
