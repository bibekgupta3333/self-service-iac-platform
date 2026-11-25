# terraform test suite for modules/sqs. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/sqs test

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
    sqs        = "http://localhost:4566"
    sts        = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-sqs"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
}

run "secure_defaults" {
  command = apply

  assert {
    condition     = aws_sqs_queue.this.name == "platform-development-tftest-sqs"
    error_message = "Queue name must be <team>-<environment>-<name>."
  }

  assert {
    condition     = aws_sqs_queue.dlq.name == "platform-development-tftest-sqs-dlq"
    error_message = "A dead-letter queue must always be created."
  }

  assert {
    condition     = jsondecode(aws_sqs_queue.this.redrive_policy).deadLetterTargetArn == aws_sqs_queue.dlq.arn
    error_message = "The main queue must redrive to the DLQ."
  }

  assert {
    condition     = jsondecode(aws_sqs_queue.this.redrive_policy).maxReceiveCount == 5
    error_message = "maxReceiveCount must default to 5."
  }

  assert {
    condition     = jsondecode(aws_sqs_queue.dlq.redrive_allow_policy).sourceQueueArns == [aws_sqs_queue.this.arn]
    error_message = "Only the main queue may redrive into the DLQ."
  }

  assert {
    condition     = aws_sqs_queue.this.kms_master_key_id == aws_kms_key.this[0].arn && aws_sqs_queue.dlq.kms_master_key_id == aws_kms_key.this[0].arn
    error_message = "Both queues must use the module-created KMS key."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation
    error_message = "The module-created key must have rotation enabled."
  }

  assert {
    condition     = strcontains(aws_sqs_queue_policy.this.policy, "aws:SecureTransport") && strcontains(aws_sqs_queue_policy.dlq.policy, "aws:SecureTransport")
    error_message = "Both queue policies must deny non-TLS requests."
  }

  assert {
    condition = alltrue([
      for k in ["iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by"] :
      contains(keys(aws_sqs_queue.this.tags_all), k) && contains(keys(aws_sqs_queue.dlq.tags_all), k)
    ])
    error_message = "Both queues must carry every mandatory iacp:* tag."
  }

  assert {
    condition     = aws_sqs_queue.dlq.tags_all["iacp:role"] == "dlq"
    error_message = "The DLQ must be tagged iacp:role=dlq."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.dlq_not_empty.threshold == 0 && aws_cloudwatch_metric_alarm.dlq_not_empty.dimensions["QueueName"] == aws_sqs_queue.dlq.name
    error_message = "An alarm must fire when the DLQ holds any message."
  }

  assert {
    condition     = output.id == aws_sqs_queue.this.url && output.dlq_url == aws_sqs_queue.dlq.url
    error_message = "id and dlq_url outputs must be queue URLs."
  }

  assert {
    condition     = strcontains(output.consumer_policy_json, "sqs:ReceiveMessage") && !strcontains(output.consumer_policy_json, "sqs:SendMessage")
    error_message = "consumer_policy_json must grant receive but not send."
  }

  assert {
    condition     = strcontains(output.producer_policy_json, "sqs:SendMessage") && !strcontains(output.producer_policy_json, "sqs:DeleteMessage")
    error_message = "producer_policy_json must grant send but not delete."
  }
}

run "fifo_queues" {
  command = apply

  variables {
    name                        = "tftest-fifo"
    fifo                        = true
    content_based_deduplication = true
    max_receive_count           = 3
  }

  assert {
    condition     = aws_sqs_queue.this.fifo_queue && aws_sqs_queue.dlq.fifo_queue
    error_message = "fifo = true must make both queues FIFO."
  }

  assert {
    condition     = endswith(aws_sqs_queue.this.name, ".fifo") && aws_sqs_queue.dlq.name == "platform-development-tftest-fifo-dlq.fifo"
    error_message = "FIFO queue names must end in .fifo."
  }

  assert {
    condition     = jsondecode(aws_sqs_queue.this.redrive_policy).maxReceiveCount == 3
    error_message = "max_receive_count must flow into the redrive policy."
  }
}

run "rejects_invalid_name" {
  command = plan

  variables {
    name = "UPPER"
  }

  expect_failures = [var.name]
}

run "rejects_dlq_retention_shorter_than_main" {
  command = plan

  variables {
    message_retention_seconds = 604800
    dlq_retention_seconds     = 86400
  }

  expect_failures = [var.dlq_retention_seconds]
}

run "rejects_dedup_without_fifo" {
  command = plan

  variables {
    content_based_deduplication = true
  }

  expect_failures = [var.content_based_deduplication]
}

run "rejects_out_of_range_max_receive_count" {
  command = plan

  variables {
    max_receive_count = 0
  }

  expect_failures = [var.max_receive_count]
}

run "rejects_out_of_range_visibility_timeout" {
  command = plan

  variables {
    visibility_timeout_seconds = 50000
  }

  expect_failures = [var.visibility_timeout_seconds]
}

run "rejects_reserved_tag_prefix" {
  command = plan

  variables {
    tags = { "iacp:managed-by" = "someone" }
  }

  expect_failures = [var.tags]
}
