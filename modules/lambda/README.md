# modules/lambda

Lambda function with a least-privilege execution role, encrypted logs and
environment, tracing and an optional SQS trigger. Part of the iacp module
registry (see [docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "worker" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/lambda?ref=modules/lambda/v2.0.0"

  name        = "order-worker"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  runtime         = "python3.12"
  handler         = "app.handler"
  artifact_s3_uri = "s3://payments-development-artifacts/order-worker/1.4.2.zip"

  # Wired from the outputs of an sqs stack.
  sqs_trigger = {
    queue_arn   = "arn:aws:sqs:us-east-1:123456789012:payments-development-orders"
    kms_key_arn = "arn:aws:kms:us-east-1:123456789012:key/1111aaaa-22bb-33cc-44dd-555555eeeeee"
    batch_size  = 10
  }

  # Extra access from other stacks, keyed by a stable name.
  extra_policy_jsons = {
    artifacts-read = var.artifacts_read_policy_json
  }
}
```

The module declares no provider. The calling root configures `provider "aws"`.
[`examples/complete`](examples/complete) wires the s3, sqs and lambda modules
together on LocalStack and deploys a small inline Python handler.

## Secure defaults

| Control | Implementation |
|---|---|
| Least privilege | Inline role policy limited to the function's own log group, its KMS key, X-Ray, the trigger queue and its key, and the dead-letter target and its key. `extra_policy_jsons` rejects `*`, `<service>:*`, other non-read wildcards, `NotAction` and `NotResource` in Allow statements, and `iam:PassRole` or any `iam:` action on `Resource "*"` (same rule as `ecs-service`). |
| IAM guardrails | The role lives under `/iacp/` (the platform's `iam:PassRole` is limited to that path) and always has a permissions boundary: `permissions_boundary_arn`, or the account's `iacp-boundary` policy by default. |
| Confused deputy | The trust policy only lets `lambda.amazonaws.com` assume the role for the owning account (`aws:SourceAccount`). |
| Encryption | Environment variables and the log group use a KMS key (module-created with rotation unless `kms_key_arn` is set). The key grants CloudWatch Logs access to this log group only. |
| Observability | Log group created up front with retention (default 30 days). X-Ray tracing `Active` by default and required in production. |
| Blast radius | Reserved concurrency set by default (10), 1-1000, and required in production (0 is rejected because it stops the function). No function URL. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last, on every taggable resource including the event source mapping (hence AWS provider >= 5.70). |

`sqs_trigger` is an object rather than a plain ARN so the queue can be created
in the same configuration: whether the trigger exists depends only on the object
being non-null, which Terraform knows at plan time even when the ARN is not.

Queues from the sqs module are encrypted with their own key. Pass it as
`sqs_trigger.kms_key_arn` (and `dead_letter_target_kms_key_arn` for an SQS
dead-letter target) so the role can decrypt trigger messages and publish
failed invocations; without it, messages cannot be read and async failures are
dropped. Lambda also requires the queue's visibility timeout to be at least the
function timeout, and SQS allows `batch_size` above 10 only with a batching
window (never on FIFO queues).

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical function name. The final name is `<team>-<environment>-<name>` (at most 64 characters). | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `runtime` | `python3.12`, `python3.13`, `nodejs20.x`, `nodejs22.x`, `java21` or `provided.al2023`. | `string` | n/a | yes |
| `handler` | Function entry point, for example `app.handler`. | `string` | `"bootstrap"` | no |
| `architecture` | `arm64` or `x86_64`. | `string` | `"arm64"` | no |
| `artifact_s3_uri` | `s3://bucket/key.zip` of the deployment package. | `string` | n/a | yes |
| `source_code_hash` | Base64 SHA-256 of the package. Changing it redeploys the code. | `string` | `null` | no |
| `memory_mb` | Memory in MB (128-10240). | `number` | `256` | no |
| `timeout_seconds` | Maximum execution time (1-900). | `number` | `30` | no |
| `reserved_concurrent_executions` | Reserved concurrency (1-1000). Null leaves the function unreserved and is rejected in production. | `number` | `10` | no |
| `tracing_mode` | `Active` or `PassThrough`. Only `Active` in production. | `string` | `"Active"` | no |
| `environment_variables` | Non-secret configuration. Keys may not start with `AWS_`. | `map(string)` | `{}` | no |
| `log_retention_days` | CloudWatch Logs retention (a supported value such as 7, 14, 30, 90, 365). | `number` | `30` | no |
| `kms_key_arn` | Existing KMS key ARN for environment variables and logs. If null, a dedicated key is created. Must be known at plan time. | `string` | `null` | no |
| `sqs_trigger` | Optional SQS trigger: `{ queue_arn, kms_key_arn = optional, batch_size = optional(10), maximum_batching_window_in_seconds = optional(0) }`. `batch_size` is 1-10, or up to 10000 with a window of 1-300 s (FIFO stays at 10). | `object` | `null` | no |
| `dead_letter_target_arn` | SQS queue or SNS topic ARN for failed asynchronous invocations. | `string` | `null` | no |
| `dead_letter_target_kms_key_arn` | KMS key of the dead-letter target; the role gets `kms:GenerateDataKey` and `kms:Decrypt`. Requires `dead_letter_target_arn`. | `string` | `null` | no |
| `permissions_boundary_arn` | Permissions boundary for the role. Null uses `arn:<partition>:iam::<account>:policy/iacp-boundary`; a boundary is always attached. | `string` | `null` | no |
| `extra_policy_jsons` | Up to 10 additional IAM policy documents keyed by a stable name (`^[a-z0-9][a-z0-9-]{0,39}$`). Allow statements may not use `*`, `<service>:*`, non-read wildcards, `NotAction` or `NotResource`, may not allow `iam:PassRole`, and may not allow an `iam:` action on `Resource "*"`. | `map(string)` | `{}` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Function name. |
| `arn` | Function ARN. |
| `name` | Computed function name (`<team>-<environment>-<name>`). |
| `role_arn` | Execution role ARN. |
| `invoke_arn` | ARN used by API Gateway and other services to invoke the function. |
| `log_group_name` | CloudWatch log group name. |
| `kms_key_arn` | ARN of the KMS key encrypting environment variables and logs. |
| `iacp_metadata` | `{ module = "lambda", version = "2.0.0" }` for inventory and drift correlation. |

## Resources

`aws_lambda_function`, `aws_iam_role`, `aws_iam_role_policy` (execution, plus
one per `extra_policy_jsons` key), `aws_cloudwatch_log_group`, optional
`aws_lambda_event_source_mapping`, and optional `aws_kms_key` + `aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009). The suite's `tests/setup` fixture
builds an artifact bucket and trigger queue with the s3 and sqs modules:

```bash
docker compose up -d localstack
terraform -chdir=modules/lambda init -backend=false
terraform -chdir=modules/lambda test
```
