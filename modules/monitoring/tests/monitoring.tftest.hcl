# terraform test suite for modules/monitoring. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/monitoring test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    cloudwatch = "http://localhost:4566"
    iam        = "http://localhost:4566"
    kms        = "http://localhost:4566"
    sns        = "http://localhost:4566"
    sqs        = "http://localhost:4566"
    sts        = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-mon"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
}

run "setup" {
  module {
    source = "./tests/setup"
  }
}

run "topic_only" {
  command = apply

  assert {
    condition     = aws_sns_topic.this.name == "platform-development-tftest-mon"
    error_message = "Topic name must be <team>-<environment>-<name>."
  }

  assert {
    condition     = aws_sns_topic.this.kms_master_key_id == aws_kms_key.this[0].arn
    error_message = "The topic must be encrypted with the module-created KMS key."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation
    error_message = "The module-created key must have rotation enabled."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_kms_key.this[0].policy).Statement :
      s.Sid == "PublishingServicesForThisAccount" && flatten([s.Principal.Service]) == ["cloudwatch.amazonaws.com"] && s.Condition.StringEquals["aws:SourceAccount"] != null
    ])
    error_message = "The key policy must let CloudWatch use the key for this account only."
  }

  assert {
    condition     = strcontains(aws_sns_topic_policy.this.policy, "aws:SecureTransport") && strcontains(aws_sns_topic_policy.this.policy, "cloudwatch.amazonaws.com")
    error_message = "The topic policy must allow CloudWatch to publish and deny non-TLS requests."
  }

  assert {
    condition     = !strcontains(aws_sns_topic_policy.this.policy, "\"Effect\":\"Allow\",\"Principal\":\"*\"") && !strcontains(aws_sns_topic_policy.this.policy, "\"AWS\":\"*\"")
    error_message = "The topic policy must not allow anonymous principals."
  }

  assert {
    condition     = length(aws_cloudwatch_dashboard.this) == 0 && output.dashboard_name == null && output.dashboard_url == null
    error_message = "No dashboard may be created without widgets or alarms."
  }

  assert {
    condition     = length(aws_sns_topic_subscription.this) == 0 && length(aws_cloudwatch_metric_alarm.this) == 0
    error_message = "No subscriptions or alarms may exist without inputs."
  }

  assert {
    condition     = { for k, v in aws_sns_topic.this.tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "monitoring" }
    error_message = "The topic must carry exactly the platform tags with the stack's values."
  }

  assert {
    condition     = { for k, v in aws_kms_key.this[0].tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "monitoring" }
    error_message = "The KMS key must carry the platform tags with the stack's values."
  }

  assert {
    condition     = output.sns_topic_arn == aws_sns_topic.this.arn && output.id == aws_sns_topic.this.arn
    error_message = "sns_topic_arn and id must be the topic ARN."
  }

  assert {
    condition     = output.iacp_metadata == { module = "monitoring", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "alarms_dashboard_subscriptions" {
  command = apply

  variables {
    name = "tftest-mon-full"
    subscriptions = {
      ops-queue = { protocol = "sqs", endpoint = run.setup.queue_arn, raw_message_delivery = true }
    }
    sqs_queue_alarms = {
      orders = { queue_name = run.setup.queue_name, oldest_message_age_seconds = 600, visible_messages_threshold = 100 }
    }
    lambda_function_alarms = {
      worker = { function_name = "platform-development-worker" }
    }
    dynamodb_table_alarms = {
      orders = { table_name = "platform-development-orders", throttle_events_threshold = 5 }
    }
    dashboard_widgets = [
      { title = "About", markdown = "Owned by **platform**.", width = 24, height = 2 },
      {
        title = "Queue depth"
        metrics = [
          { namespace = "AWS/SQS", metric_name = "ApproximateNumberOfMessagesVisible", dimensions = { QueueName = run.setup.queue_name }, stat = "Maximum", label = "visible" },
        ]
      },
    ]
  }

  assert {
    condition = toset(keys(aws_cloudwatch_metric_alarm.this)) == toset([
      "sqs-orders-oldest-message-age", "sqs-orders-visible-messages",
      "lambda-worker-errors", "lambda-worker-throttles",
      "dynamodb-orders-read-throttles", "dynamodb-orders-write-throttles",
    ])
    error_message = "Every alarm helper entry must produce its alarms."
  }

  assert {
    condition = alltrue([
      for a in aws_cloudwatch_metric_alarm.this :
      a.alarm_actions == toset([aws_sns_topic.this.arn]) && a.ok_actions == toset([aws_sns_topic.this.arn]) && startswith(a.alarm_name, "platform-development-tftest-mon-full-")
    ])
    error_message = "Every alarm must be named after the instance and notify the topic on alarm and recovery."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.this["sqs-orders-oldest-message-age"].threshold == 600 && aws_cloudwatch_metric_alarm.this["sqs-orders-oldest-message-age"].dimensions["QueueName"] == run.setup.queue_name
    error_message = "The SQS age alarm must use the queue and threshold from the input."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.this["lambda-worker-errors"].metric_name == "Errors" && aws_cloudwatch_metric_alarm.this["lambda-worker-errors"].threshold == 1 && aws_cloudwatch_metric_alarm.this["lambda-worker-errors"].comparison_operator == "GreaterThanOrEqualToThreshold"
    error_message = "The Lambda error alarm must fire on the first error by default."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.this["dynamodb-orders-write-throttles"].metric_name == "WriteThrottleEvents" && aws_cloudwatch_metric_alarm.this["dynamodb-orders-write-throttles"].threshold == 5
    error_message = "The DynamoDB write throttle alarm must use the input threshold."
  }

  assert {
    condition     = output.dashboard_name == "platform-development-tftest-mon-full" && output.dashboard_url != null
    error_message = "A dashboard named after the instance must be created."
  }

  assert {
    condition     = [for w in jsondecode(aws_cloudwatch_dashboard.this[0].dashboard_body).widgets : w.type] == ["text", "metric", "alarm"]
    error_message = "Widgets must keep their order and be followed by the alarm status widget."
  }

  assert {
    condition     = toset(jsondecode(aws_cloudwatch_dashboard.this[0].dashboard_body).widgets[2].properties.alarms) == toset(values(output.alarm_arns))
    error_message = "The alarm status widget must list every alarm."
  }

  assert {
    condition     = jsondecode(aws_cloudwatch_dashboard.this[0].dashboard_body).widgets[1].properties.metrics[0] == ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", run.setup.queue_name, { stat = "Maximum", label = "visible" }]
    error_message = "Metric widgets must render namespace, metric, dimensions and options in CloudWatch's array form."
  }

  assert {
    condition     = aws_sns_topic_subscription.this["ops-queue"].protocol == "sqs" && aws_sns_topic_subscription.this["ops-queue"].endpoint == run.setup.queue_arn && aws_sns_topic_subscription.this["ops-queue"].raw_message_delivery
    error_message = "Subscriptions must be created from the input."
  }
}

# LocalStack 4.4 does not persist CloudWatch alarm tags, so alarm tags are
# asserted on planned values (what the policy gate sees) instead of state.
run "planned_alarm_tags" {
  command = plan

  variables {
    lambda_function_alarms = {
      worker = { function_name = "platform-development-worker" }
    }
  }

  assert {
    condition = alltrue([
      for a in aws_cloudwatch_metric_alarm.this :
      { for k, v in a.tags : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "monitoring" }
    ])
    error_message = "Every alarm must be configured with the platform tags and the stack's values."
  }
}

run "existing_key" {
  command = plan

  variables {
    kms_key_arn = "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && aws_sns_topic.this.kms_master_key_id == "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
    error_message = "An existing kms_key_arn must be used instead of creating a key."
  }
}

run "extra_publishers" {
  command = plan

  variables {
    publisher_service_principals = ["events.amazonaws.com"]
  }

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.kms[0].json).Statement :
      s.Sid == "PublishingServicesForThisAccount" && toset(flatten([s.Principal.Service])) == toset(["cloudwatch.amazonaws.com", "events.amazonaws.com"])
    ])
    error_message = "publisher_service_principals must be able to use the key next to CloudWatch."
  }

  assert {
    condition     = tolist(local.publisher_services) == tolist(["cloudwatch.amazonaws.com", "events.amazonaws.com"])
    error_message = "publisher_service_principals must be allowed to publish next to CloudWatch."
  }
}

run "rejects_http_subscription" {
  command = plan

  variables {
    subscriptions = { hook = { protocol = "http", endpoint = "http://example.com/hook" } }
  }

  expect_failures = [var.subscriptions]
}

run "rejects_mismatched_endpoint" {
  command = plan

  variables {
    subscriptions = { ops = { protocol = "email", endpoint = "https://example.com" } }
  }

  expect_failures = [var.subscriptions]
}

run "rejects_raw_delivery_for_email" {
  command = plan

  variables {
    subscriptions = { ops = { protocol = "email", endpoint = "ops@example.com", raw_message_delivery = true } }
  }

  expect_failures = [var.subscriptions]
}

run "rejects_widget_with_metrics_and_markdown" {
  command = plan

  variables {
    dashboard_widgets = [
      { title = "Both", markdown = "x", metrics = [{ namespace = "AWS/SQS", metric_name = "NumberOfMessagesSent" }] },
    ]
  }

  expect_failures = [var.dashboard_widgets]
}

run "rejects_empty_widget" {
  command = plan

  variables {
    dashboard_widgets = [{ title = "Nothing" }]
  }

  expect_failures = [var.dashboard_widgets]
}

run "rejects_invalid_stat_and_period" {
  command = plan

  variables {
    dashboard_widgets = [
      { title = "Bad", period = 45, metrics = [{ namespace = "AWS/SQS", metric_name = "NumberOfMessagesSent", stat = "Median" }] },
    ]
  }

  expect_failures = [var.dashboard_widgets]
}

run "rejects_short_age_threshold" {
  command = plan

  variables {
    sqs_queue_alarms = { orders = { queue_name = "orders", oldest_message_age_seconds = 30 } }
  }

  expect_failures = [var.sqs_queue_alarms]
}

run "rejects_zero_lambda_threshold" {
  command = plan

  variables {
    lambda_function_alarms = { worker = { function_name = "worker", errors_threshold = 0 } }
  }

  expect_failures = [var.lambda_function_alarms]
}

run "rejects_invalid_alarm_key" {
  command = plan

  variables {
    dynamodb_table_alarms = { "Orders Table" = { table_name = "orders" } }
  }

  expect_failures = [var.dynamodb_table_alarms]
}

run "rejects_wildcard_publisher" {
  command = plan

  variables {
    publisher_service_principals = ["*"]
  }

  expect_failures = [var.publisher_service_principals]
}

run "rejects_reserved_tag_prefix" {
  command = plan

  variables {
    tags = { "iacp:managed-by" = "someone" }
  }

  expect_failures = [var.tags]
}
