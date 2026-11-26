# modules/sqs

KMS-encrypted SQS queue with a mandatory dead-letter queue. Part of the iacp
module registry (see [docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "orders" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/sqs?ref=modules/sqs/v1.0.0"

  name        = "orders"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  max_receive_count = 3
}
```

The module declares no provider. The calling root configures `provider "aws"`.
[`examples/complete`](examples/complete) shows a LocalStack configuration.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | SSE-KMS on both queues. A dedicated key with rotation is created unless `kms_key_arn` is set. |
| Encryption in transit | Queue policies deny any request with `aws:SecureTransport = false`. |
| Poison messages | A DLQ is always created. `redrive_allow_policy` lets only the main queue use it. |
| Observability | Alarms on DLQ depth > 0 and on the age of the oldest message, notifying `alarm_actions`. |
| Least privilege | No wildcard principals. Consumers and producers attach `consumer_policy_json` / `producer_policy_json`. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last; the DLQ also has `iacp:role=dlq`. |

By default the module-created key only trusts IAM principals in the owning
account. If another AWS service (SNS, EventBridge) must send to the queue, list
it in `key_service_principals` (scoped to this account with
`aws:SourceAccount`), or pass a `kms_key_arn` whose policy allows it. The
service also needs a queue policy grant, which this version does not create.

The example's alarm topic is encrypted with a customer managed key that grants
`cloudwatch.amazonaws.com`. The AWS-managed `alias/aws/sns` key cannot be
granted to CloudWatch, so alarms publishing to such a topic never notify.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical queue name. The final name is `<team>-<environment>-<name>` (plus `.fifo`). | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `fifo` | Create FIFO queues (main and DLQ). | `bool` | `false` | no |
| `content_based_deduplication` | Deduplicate on a SHA-256 of the body. Requires `fifo`. | `bool` | `false` | no |
| `visibility_timeout_seconds` | Seconds a received message stays hidden (0-43200). | `number` | `30` | no |
| `message_retention_seconds` | Seconds the main queue keeps a message (60-1209600). | `number` | `345600` | no |
| `receive_wait_time_seconds` | Long-polling wait time (0-20). | `number` | `20` | no |
| `max_receive_count` | Receives before a message moves to the DLQ (1-100). | `number` | `5` | no |
| `dlq_retention_seconds` | Seconds the DLQ keeps a message. At least `message_retention_seconds`. | `number` | `1209600` | no |
| `kms_key_arn` | Existing KMS key ARN. If null, a dedicated key is created. | `string` | `null` | no |
| `key_service_principals` | AWS service principals (for example `sns.amazonaws.com`) allowed to use the module-created key for this account. Ignored with `kms_key_arn`. | `list(string)` | `[]` | no |
| `kms_data_key_reuse_period_seconds` | Seconds SQS reuses a data key (60-86400). | `number` | `300` | no |
| `alarm_actions` | ARNs notified when a queue alarm changes state. | `list(string)` | `[]` | no |
| `oldest_message_age_alarm_seconds` | Alarm threshold for the oldest message age (>= 60). | `number` | `3600` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Main queue URL. |
| `arn` | Main queue ARN. |
| `name` | Main queue name. |
| `dlq_arn` | Dead-letter queue ARN. |
| `dlq_url` | Dead-letter queue URL. |
| `kms_key_arn` | ARN of the KMS key encrypting both queues. |
| `consumer_policy_json` | IAM policy document for consumers (receive, delete, change visibility, decrypt). |
| `producer_policy_json` | IAM policy document for producers (send, generate data key). |
| `iacp_metadata` | `{ module = "sqs", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_sqs_queue` (main and DLQ, with inline redrive and redrive allow policies),
`aws_sqs_queue_policy` (x2), `aws_cloudwatch_metric_alarm` (x2), and optional
`aws_kms_key` + `aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009):

```bash
docker compose up -d localstack
terraform -chdir=modules/sqs init -backend=false
terraform -chdir=modules/sqs test
cd tests && go test -run TestSQS ./...
```
