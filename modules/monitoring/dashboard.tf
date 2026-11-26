# Dashboards cannot be tagged; the name carries the ownership instead. Widgets
# without x/y are laid out by CloudWatch in order. Alarm status widgets hold at
# most 100 alarms each.
resource "aws_cloudwatch_dashboard" "this" {
  count = local.create_dashboard ? 1 : 0

  dashboard_name = local.full_name
  dashboard_body = jsonencode({
    widgets = concat(
      local.user_widgets,
      [
        for i, arns in chunklist(sort([for a in aws_cloudwatch_metric_alarm.this : a.arn]), 100) : {
          type   = "alarm"
          width  = 24
          height = 6
          properties = {
            title  = i == 0 ? "Alarms" : "Alarms (${i + 1})"
            alarms = arns
          }
        }
      ],
    )
  })
}
