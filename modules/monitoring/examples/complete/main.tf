# An alarm topic, dashboard and alarms for a queue, wired the way the platform
# wires stacks: the sqs module's own alarms notify the monitoring topic, and the
# monitoring module alarms on the queue by name.

module "queue" {
  source = "../../../sqs"

  name        = "${var.name}-jobs"
  team        = var.team
  environment = var.environment
  stack       = var.stack

  alarm_actions = [module.monitoring.sns_topic_arn]
}

module "monitoring" {
  source = "../../"

  name        = var.name
  team        = var.team
  environment = var.environment
  stack       = var.stack

  subscriptions = {
    on-call = { protocol = "email", endpoint = "on-call@example.com" }
  }

  sqs_queue_alarms = {
    jobs = { queue_name = module.queue.name, oldest_message_age_seconds = 600, visible_messages_threshold = 1000 }
  }

  lambda_function_alarms = {
    worker = { function_name = "${var.team}-${var.environment}-${var.name}-worker" }
  }

  # An alarm on an application's own metric, published under a custom namespace.
  metric_alarms = [
    {
      name        = "jobs-failed"
      namespace   = "Example/Jobs"
      metric_name = "JobsFailed"
      dimensions  = { Queue = module.queue.name }
      statistic   = "Sum"
      threshold   = 5
      comparison  = "GreaterThanOrEqualToThreshold"
      period      = 600
    },
  ]

  dashboard_widgets = [
    { title = "Jobs", markdown = "Queue health for the jobs pipeline.", width = 24, height = 2 },
    {
      title = "Queue backlog"
      metrics = [
        { namespace = "AWS/SQS", metric_name = "ApproximateNumberOfMessagesVisible", dimensions = { QueueName = module.queue.name }, stat = "Maximum", label = "visible" },
        { namespace = "AWS/SQS", metric_name = "ApproximateAgeOfOldestMessage", dimensions = { QueueName = module.queue.name }, stat = "Maximum", label = "oldest (s)" },
      ]
    },
  ]

  tags = {
    "cost-center" = "cc-1234"
  }
}
