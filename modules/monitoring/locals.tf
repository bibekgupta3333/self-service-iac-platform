locals {
  module_name    = "monitoring"
  module_version = "1.0.0"

  full_name     = "${var.team}-${var.environment}-${var.name}"
  is_production = var.environment == "production"

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn

  # CloudWatch always publishes alarms; callers may add other AWS services.
  publisher_services = distinct(concat(["cloudwatch.amazonaws.com"], var.publisher_service_principals))

  # ---- Alarm helpers: every helper input becomes one or more alarm specs ----
  # Keys are "<kind>-<input key>-<metric>", unique across kinds and stable when
  # entries are added or removed.

  sqs_alarms = merge(
    {
      for k, a in var.sqs_queue_alarms : "sqs-${k}-oldest-message-age" => {
        description = "The oldest message in ${a.queue_name} is older than ${a.oldest_message_age_seconds}s."
        namespace   = "AWS/SQS"
        metric_name = "ApproximateAgeOfOldestMessage"
        dimensions  = { QueueName = a.queue_name }
        statistic   = "Maximum"
        threshold   = a.oldest_message_age_seconds
        comparison  = "GreaterThanThreshold"
      }
    },
    {
      for k, a in var.sqs_queue_alarms : "sqs-${k}-visible-messages" => {
        description = "More than ${a.visible_messages_threshold} messages are waiting in ${a.queue_name}."
        namespace   = "AWS/SQS"
        metric_name = "ApproximateNumberOfMessagesVisible"
        dimensions  = { QueueName = a.queue_name }
        statistic   = "Maximum"
        threshold   = a.visible_messages_threshold
        comparison  = "GreaterThanThreshold"
      } if a.visible_messages_threshold != null
    },
  )

  lambda_alarms = merge([
    for k, a in var.lambda_function_alarms : {
      "lambda-${k}-errors" = {
        description = "${a.function_name} reported at least ${a.errors_threshold} errors in 5 minutes."
        namespace   = "AWS/Lambda"
        metric_name = "Errors"
        dimensions  = { FunctionName = a.function_name }
        statistic   = "Sum"
        threshold   = a.errors_threshold
        comparison  = "GreaterThanOrEqualToThreshold"
      }
      "lambda-${k}-throttles" = {
        description = "${a.function_name} was throttled at least ${a.throttles_threshold} times in 5 minutes."
        namespace   = "AWS/Lambda"
        metric_name = "Throttles"
        dimensions  = { FunctionName = a.function_name }
        statistic   = "Sum"
        threshold   = a.throttles_threshold
        comparison  = "GreaterThanOrEqualToThreshold"
      }
    }
  ]...)

  dynamodb_alarms = merge([
    for k, a in var.dynamodb_table_alarms : {
      for op in ["read", "write"] : "dynamodb-${k}-${op}-throttles" => {
        description = "${a.table_name} had at least ${a.throttle_events_threshold} ${op} throttle events in 5 minutes."
        namespace   = "AWS/DynamoDB"
        metric_name = op == "read" ? "ReadThrottleEvents" : "WriteThrottleEvents"
        dimensions  = { TableName = a.table_name }
        statistic   = "Sum"
        threshold   = a.throttle_events_threshold
        comparison  = "GreaterThanOrEqualToThreshold"
      }
    }
  ]...)

  alarms = merge(local.sqs_alarms, local.lambda_alarms, local.dynamodb_alarms)

  # ---- Dashboard ----
  # Each widget is rendered through jsonencode so text and metric widgets (which
  # have different shapes) can share one list.

  user_widgets = [
    for w in var.dashboard_widgets : jsondecode(w.markdown != null ? jsonencode({
      type       = "text"
      width      = w.width
      height     = w.height
      properties = { markdown = "### ${w.title}\n\n${w.markdown}" }
      }) : jsonencode({
      type   = "metric"
      width  = w.width
      height = w.height
      properties = {
        title   = w.title
        region  = data.aws_region.current.name
        view    = w.view
        stacked = w.stacked
        period  = w.period
        metrics = [
          for m in w.metrics : concat(
            [m.namespace, m.metric_name],
            flatten([for dk in sort(keys(m.dimensions)) : [dk, m.dimensions[dk]]]),
            [{ for k, v in { stat = m.stat, label = m.label } : k => v if v != null }],
          )
        ]
      }
    }))
  ]

  create_dashboard = length(var.dashboard_widgets) + length(local.alarms) > 0
}
