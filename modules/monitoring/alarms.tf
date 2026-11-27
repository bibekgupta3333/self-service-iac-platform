# One alarm per spec built in locals.tf from the alarm helper inputs and
# metric_alarms. Every alarm notifies the topic when it fires and when it
# recovers.
resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "${local.full_name}-${each.key}"
  alarm_description   = each.value.description
  namespace           = each.value.namespace
  metric_name         = each.value.metric_name
  dimensions          = each.value.dimensions
  statistic           = each.value.statistic
  extended_statistic  = each.value.extended_statistic
  period              = each.value.period
  evaluation_periods  = each.value.evaluation_periods
  threshold           = each.value.threshold
  comparison_operator = each.value.comparison
  treat_missing_data  = each.value.treat_missing_data
  actions_enabled     = true
  alarm_actions       = [aws_sns_topic.this.arn]
  ok_actions          = [aws_sns_topic.this.arn]
  tags                = local.tags
}
