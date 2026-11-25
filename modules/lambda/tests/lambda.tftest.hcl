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
    condition     = length(aws_lambda_event_source_mapping.sqs) == 0
    error_message = "No event source mapping may exist without sqs_trigger."
  }

  assert {
    condition     = !strcontains(aws_iam_role_policy.this.policy, "\"Action\":\"*\"") && !strcontains(aws_iam_role_policy.this.policy, "\"logs:*\"")
    error_message = "The execution policy must not use wildcard actions."
  }

  assert {
    condition     = strcontains(aws_iam_role_policy.this.policy, aws_cloudwatch_log_group.this.arn)
    error_message = "Log permissions must be scoped to the function's own log group."
  }

  assert {
    condition = alltrue([
      for k in ["iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by"] :
      contains(keys(aws_lambda_function.this.tags_all), k) && contains(keys(aws_iam_role.this.tags_all), k) && contains(keys(aws_cloudwatch_log_group.this.tags_all), k)
    ])
    error_message = "Function, role and log group must carry every mandatory iacp:* tag."
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
      queue_arn  = run.setup.queue_arn
      batch_size = 5
    }
    extra_policy_jsons = [run.setup.consumer_policy_json]
  }

  assert {
    condition     = aws_lambda_event_source_mapping.sqs[0].event_source_arn == run.setup.queue_arn && aws_lambda_event_source_mapping.sqs[0].batch_size == 5
    error_message = "sqs_trigger must create an event source mapping with the requested batch size."
  }

  assert {
    condition     = contains(aws_lambda_event_source_mapping.sqs[0].function_response_types, "ReportBatchItemFailures")
    error_message = "SQS mappings must report partial batch failures."
  }

  assert {
    condition     = strcontains(aws_iam_role_policy.this.policy, run.setup.queue_arn)
    error_message = "The execution role must be allowed to consume only the trigger queue."
  }

  assert {
    condition     = length(aws_iam_role_policy.extra) == 1
    error_message = "Each extra policy document must be attached to the role."
  }
}

run "rejects_wildcard_extra_policy" {
  command = plan

  variables {
    artifact_s3_uri    = "s3://bucket-name/key.zip"
    extra_policy_jsons = [jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = "*", Resource = "*" }] })]
  }

  expect_failures = [var.extra_policy_jsons]
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

run "rejects_unreserved_concurrency_in_production" {
  command = plan

  variables {
    artifact_s3_uri                = "s3://bucket-name/key.zip"
    environment                    = "production"
    reserved_concurrent_executions = -1
  }

  expect_failures = [var.reserved_concurrent_executions]
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
