# terraform test suite for modules/lambda. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/lambda test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    cloudwatch = "http://localhost:4566"
    iam        = "http://localhost:4566"
    kms        = "http://localhost:4566"
    lambda     = "http://localhost:4566"
    logs       = "http://localhost:4566"
    s3         = "http://localhost:4566"
    sqs        = "http://localhost:4566"
    sts        = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-fn"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  runtime     = "python3.12"
  handler     = "app.handler"
}

run "setup" {
  module {
    source = "./tests/setup"
  }
}

run "secure_defaults" {
  command = apply

  variables {
    artifact_s3_uri  = run.setup.artifact_s3_uri
    source_code_hash = run.setup.source_code_hash
    environment_variables = {
      LOG_LEVEL = "info"
    }
  }

  assert {
    condition     = aws_lambda_function.this.function_name == "platform-development-tftest-fn"
    error_message = "Function name must be <team>-<environment>-<name>."
  }

  assert {
    condition     = one(aws_lambda_function.this.tracing_config).mode == "Active"
    error_message = "X-Ray tracing must be active by default."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation
    error_message = "The module-created key must have rotation enabled."
  }

  assert {
    condition     = aws_cloudwatch_log_group.this.name == "/aws/lambda/platform-development-tftest-fn" && aws_cloudwatch_log_group.this.retention_in_days == 30
    error_message = "The log group must exist with 30-day retention."
  }

  assert {
    condition     = aws_cloudwatch_log_group.this.kms_key_id == aws_kms_key.this[0].arn
    error_message = "The log group must be encrypted with the module key."
  }

  assert {
    condition     = aws_iam_role.this.path == "/iacp/"
    error_message = "The execution role must live under the /iacp/ path."
  }

  assert {
    condition     = aws_iam_role.this.permissions_boundary == "arn:aws:iam::000000000000:policy/iacp-boundary"
    error_message = "The platform boundary must be attached by default."
  }

  assert {
    condition     = length(aws_lambda_event_source_mapping.sqs) == 0
    error_message = "No event source mapping may exist without sqs_trigger."
  }

  assert {
    condition = toset(flatten([for s in jsondecode(aws_iam_role_policy.this.policy).Statement : flatten([s.Action])])) == toset([
      "logs:CreateLogStream", "logs:PutLogEvents", "kms:Decrypt", "xray:PutTraceSegments", "xray:PutTelemetryRecords",
    ])
    error_message = "The default execution policy must grant exactly logs, the function key and X-Ray."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_iam_role_policy.this.policy).Statement :
      s.Sid == "WriteTraces" || !contains(flatten([s.Resource]), "*")
    ])
    error_message = "Only the X-Ray statement may use Resource \"*\"."
  }

  assert {
    condition     = strcontains(aws_iam_role_policy.this.policy, aws_cloudwatch_log_group.this.arn)
    error_message = "Log permissions must be scoped to the function's own log group."
  }

  assert {
    condition = alltrue([
      for r in [aws_lambda_function.this, aws_iam_role.this, aws_cloudwatch_log_group.this, aws_kms_key.this[0]] :
      r.tags_all["iacp:team"] == "platform" && r.tags_all["iacp:environment"] == "development" && r.tags_all["iacp:stack"] == "tftest" && r.tags_all["iacp:managed-by"] == "iacp" && r.tags_all["iacp:module"] == "lambda"
    ])
    error_message = "Function, role, log group and key must carry the mandatory iacp:* tags with the stack's values."
  }

  assert {
    condition     = output.iacp_metadata == { module = "lambda", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

# LocalStack 4.4 does not return reserved concurrency or the environment KMS
# key on GetFunction, so these are asserted on planned values instead of
# applied state.
run "planned_concurrency_and_encryption" {
  command = plan

  variables {
    artifact_s3_uri  = run.setup.artifact_s3_uri
    source_code_hash = run.setup.source_code_hash
    environment_variables = {
      LOG_LEVEL = "info"
    }
  }

  assert {
    condition     = aws_lambda_function.this.reserved_concurrent_executions == 10
    error_message = "Reserved concurrency must be set by default."
  }

  assert {
    condition     = aws_lambda_function.this.kms_key_arn == aws_kms_key.this[0].arn
    error_message = "Environment variables must be encrypted with the module-created key."
  }
}

run "sqs_trigger" {
  command = apply

  variables {
    artifact_s3_uri  = run.setup.artifact_s3_uri
    source_code_hash = run.setup.source_code_hash
    sqs_trigger = {
      queue_arn                          = run.setup.queue_arn
      kms_key_arn                        = run.setup.queue_kms_key_arn
      batch_size                         = 50
      maximum_batching_window_in_seconds = 5
    }
    extra_policy_jsons = {
      trigger-consumer = run.setup.consumer_policy_json
    }
  }

  assert {
    condition     = aws_lambda_event_source_mapping.sqs[0].event_source_arn == run.setup.queue_arn && aws_lambda_event_source_mapping.sqs[0].batch_size == 50
    error_message = "sqs_trigger must create an event source mapping with the requested batch size."
  }

  assert {
    condition     = aws_lambda_event_source_mapping.sqs[0].maximum_batching_window_in_seconds == 5
    error_message = "maximum_batching_window_in_seconds must flow into the mapping."
  }

  assert {
    condition     = contains(aws_lambda_event_source_mapping.sqs[0].function_response_types, "ReportBatchItemFailures")
    error_message = "SQS mappings must report partial batch failures."
  }

  assert {
    condition     = aws_lambda_event_source_mapping.sqs[0].tags_all["iacp:stack"] == "tftest" && aws_lambda_event_source_mapping.sqs[0].tags_all["iacp:managed-by"] == "iacp"
    error_message = "The event source mapping must carry the mandatory iacp:* tags."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_iam_role_policy.this.policy).Statement :
      s.Sid == "ConsumeTriggerQueue" && flatten([s.Resource]) == [run.setup.queue_arn]
    ])
    error_message = "The execution role must be allowed to consume only the trigger queue."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_iam_role_policy.this.policy).Statement :
      s.Sid == "DecryptTriggerQueueMessages" && flatten([s.Resource]) == [run.setup.queue_kms_key_arn]
    ])
    error_message = "The execution role must be able to decrypt messages with the queue's key."
  }

  assert {
    condition     = keys(aws_iam_role_policy.extra) == ["trigger-consumer"] && aws_iam_role_policy.extra["trigger-consumer"].name == "extra-trigger-consumer"
    error_message = "Extra policy documents must be attached under their stable names."
  }
}

run "dead_letter_target_kms" {
  command = plan

  variables {
    artifact_s3_uri                = "s3://bucket-name/key.zip"
    dead_letter_target_arn         = "arn:aws:sqs:us-east-1:000000000000:platform-development-dlq"
    dead_letter_target_kms_key_arn = "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
  }

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.execution.json).Statement :
      s.Sid == "EncryptFailedInvocations" && toset(flatten([s.Action])) == toset(["kms:GenerateDataKey", "kms:Decrypt"])
    ])
    error_message = "A dead-letter key must grant kms:GenerateDataKey and kms:Decrypt."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.execution.json).Statement :
      s.Sid == "PublishFailedInvocations" && flatten([s.Action]) == ["sqs:SendMessage"]
    ])
    error_message = "An SQS dead-letter target must grant sqs:SendMessage."
  }
}

run "custom_boundary_is_used" {
  command = plan

  variables {
    artifact_s3_uri          = "s3://bucket-name/key.zip"
    permissions_boundary_arn = "arn:aws:iam::000000000000:policy/custom-boundary"
  }

  assert {
    condition     = aws_iam_role.this.permissions_boundary == "arn:aws:iam::000000000000:policy/custom-boundary"
    error_message = "An explicit permissions_boundary_arn must replace the default."
  }
}

run "rejects_wildcard_extra_policy" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = { all = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = "*", Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_service_wildcard_extra_policy" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = { s3-all = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["s3:GetObject", "s3:*"], Resource = "arn:aws:s3:::bucket/*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_wildcard_extra_policy" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = { iam = jsonencode({ Version = "2012-10-17", Statement = { Effect = "Allow", Action = "iam:*", Resource = "arn:aws:iam::000000000000:role/x" } }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_not_action_extra_policy" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = { not-action = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", NotAction = "iam:*", Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "accepts_read_prefix_wildcard" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = { read = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["s3:Get*", "s3:List*"], Resource = "arn:aws:s3:::bucket/*" }] }) }
  }

  assert {
    condition     = length(aws_iam_role_policy.extra) == 1
    error_message = "Read-prefix wildcards such as s3:Get* must be accepted."
  }
}

run "rejects_large_batch_without_window" {
  command = plan

  variables {
    artifact_s3_uri = "s3://bucket-name/key.zip"
    sqs_trigger = {
      queue_arn  = "arn:aws:sqs:us-east-1:000000000000:jobs"
      batch_size = 500
    }
  }

  expect_failures = [var.sqs_trigger]
}

run "rejects_large_batch_on_fifo" {
  command = plan

  variables {
    artifact_s3_uri = "s3://bucket-name/key.zip"
    sqs_trigger = {
      queue_arn                          = "arn:aws:sqs:us-east-1:000000000000:jobs.fifo"
      batch_size                         = 20
      maximum_batching_window_in_seconds = 5
    }
  }

  expect_failures = [var.sqs_trigger]
}

run "rejects_dlq_key_without_target" {
  command = plan

  variables {
    artifact_s3_uri                = "s3://bucket-name/key.zip"
    dead_letter_target_kms_key_arn = "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
  }

  expect_failures = [var.dead_letter_target_kms_key_arn]
}

run "rejects_unsupported_runtime" {
  command = plan

  variables {
    artifact_s3_uri = "s3://bucket-name/key.zip"
    runtime         = "python3.8"
  }

  expect_failures = [var.runtime]
}

run "rejects_non_zip_artifact" {
  command = plan

  variables {
    artifact_s3_uri = "https://example.com/key.tar.gz"
  }

  expect_failures = [var.artifact_s3_uri]
}

run "rejects_out_of_range_memory" {
  command = plan

  variables {
    artifact_s3_uri = "s3://bucket-name/key.zip"
    memory_mb       = 64
  }

  expect_failures = [var.memory_mb]
}

run "rejects_zero_reserved_concurrency" {
  command = plan

  variables {
    artifact_s3_uri                = "s3://bucket-name/key.zip"
    reserved_concurrent_executions = 0
  }

  expect_failures = [var.reserved_concurrent_executions]
}

run "rejects_unreserved_concurrency_in_production" {
  command = plan

  variables {
    artifact_s3_uri                = "s3://bucket-name/key.zip"
    environment                    = "production"
    reserved_concurrent_executions = null
  }

  expect_failures = [var.reserved_concurrent_executions]
}

run "rejects_passthrough_tracing_in_production" {
  command = plan

  variables {
    artifact_s3_uri = "s3://bucket-name/key.zip"
    environment     = "production"
    tracing_mode    = "PassThrough"
  }

  expect_failures = [var.tracing_mode]
}

run "rejects_reserved_env_var_prefix" {
  command = plan

  variables {
    artifact_s3_uri       = "s3://bucket-name/key.zip"
    environment_variables = { AWS_REGION = "us-east-1" }
  }

  expect_failures = [var.environment_variables]
}

run "rejects_unsupported_log_retention" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    log_retention_days = 10
  }

  expect_failures = [var.log_retention_days]
}

run "rejects_invalid_boundary" {
  command = plan

  variables {
    artifact_s3_uri          = "s3://bucket-name/key.zip"
    permissions_boundary_arn = "iacp-boundary"
  }

  expect_failures = [var.permissions_boundary_arn]
}
