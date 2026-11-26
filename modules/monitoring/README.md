# modules/monitoring

An SNS alarm topic encrypted with a customer managed key, optional
subscriptions, a CloudWatch dashboard built from typed widgets, and alarm
helpers for common SQS, Lambda and DynamoDB metrics. Part of the iacp module
registry (see [docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "alerts" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/monitoring?ref=modules/monitoring/v1.0.0"

  name        = "orders"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  subscriptions = {
    on-call = { protocol = "email", endpoint = "payments-oncall@example.com" }
  }

  sqs_queue_alarms = {
    orders = { queue_name = "payments-development-orders", oldest_message_age_seconds = 600 }
  }

  lambda_function_alarms = {
    worker = { function_name = "payments-development-orders-worker" }
  }

  dashboard_widgets = [
    {
      title   = "Orders backlog"
      metrics = [{ namespace = "AWS/SQS", metric_name = "ApproximateNumberOfMessagesVisible", dimensions = { QueueName = "payments-development-orders" }, stat = "Maximum" }]
    },
  ]
}
```

Other modules can send their own alarms to the topic: pass `sns_topic_arn` as
the sqs module's `alarm_actions`. The module declares no provider. The calling
root configures `provider "aws"`. [`examples/complete`](examples/complete)
shows a LocalStack configuration.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | The topic always uses a customer managed key. A dedicated key with rotation is created unless `kms_key_arn` is set. Its policy lets only CloudWatch (and `publisher_service_principals`) generate data keys and decrypt, for this account. The AWS-managed `alias/aws/sns` key is never used because CloudWatch cannot be granted it, so alarms would never notify. |
| Encryption in transit | The topic policy denies `sns:Publish` without TLS. Subscriptions accept `https` but never plain `http`. |
| Least privilege | The topic policy lets only CloudWatch and the listed services publish, scoped to this account with `aws:SourceAccount`. Principals that publish their own messages attach `publisher_policy_json`. |
| Observability | Each alarm notifies the topic when it fires and when it recovers. Missing data does not breach. The dashboard ends with an alarm status widget listing every alarm. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last on the topic, the key and every alarm. Dashboards cannot be tagged, so the dashboard carries the `<team>-<environment>-<name>` name instead. |

## Subscriptions

| Protocol | Endpoint | Notes |
|---|---|---|
| `email`, `email-json` | Email address | Pending until the recipient confirms. |
| `https` | `https://` URL | The endpoint must confirm the subscription. Plain `http` is rejected. |
| `lambda` | Function ARN | The module adds an `aws_lambda_permission` that lets `sns.amazonaws.com` invoke the function, only from this topic (`source_arn`) and this account. |

`sqs` is not supported in v1.0.0. SNS can only deliver to a queue whose queue
policy allows this topic, and a queue has a single policy document owned by
the queue's own stack (the sqs module writes a TLS-only policy), so this
module cannot add the grant without overwriting it. Subscribe queues from the
queue's stack instead.

## Alarm helpers

Each helper is a map keyed by a stable name. Every entry creates the alarms
below, named `<team>-<environment>-<name>-<kind>-<key>-<metric>`, with a 5-minute
period and one evaluation period.

| Input | Alarms | Defaults |
|---|---|---|
| `sqs_queue_alarms` | `ApproximateAgeOfOldestMessage` (Maximum) above `oldest_message_age_seconds`; `ApproximateNumberOfMessagesVisible` (Maximum) above `visible_messages_threshold` when it is set | 900 s; depth alarm off |
| `lambda_function_alarms` | `Errors` and `Throttles` (Sum) at or above `errors_threshold` / `throttles_threshold` | 1 and 1 |
| `dynamodb_table_alarms` | `ReadThrottleEvents` and `WriteThrottleEvents` (Sum) at or above `throttle_events_threshold` | 1 |

## Dashboard widgets

`dashboard_widgets` is an ordered list. A widget is either a metric graph
(`metrics`) or a text panel (`markdown`, at most 2,000 characters per widget
and 10,000 in total, which keeps the dashboard body well inside CloudWatch's
size limit), never both. Metrics are rendered in
CloudWatch's array form (`[namespace, metric, dim, value, …, {stat, label}]`)
for the stack's region. Widgets have no x/y position, so CloudWatch places
them in order on its 24-column grid. The dashboard is only created when there
is at least one widget or alarm.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical name. The topic and dashboard are named `<team>-<environment>-<name>`. | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `kms_key_arn` | Existing KMS key ARN whose policy lets CloudWatch use it. If null, a dedicated key is created. | `string` | `null` | no |
| `publisher_service_principals` | AWS services besides CloudWatch allowed to publish and use the key for this account (at most 10). | `list(string)` | `[]` | no |
| `subscriptions` | Up to 20 subscriptions keyed by a stable name: `protocol` (`email`, `email-json`, `https`, `lambda`), a matching `endpoint`, and `raw_message_delivery` (`https` only). | `map(object)` | `{}` | no |
| `dashboard_widgets` | Up to 100 widgets: `title`, then `metrics` (`namespace`, `metric_name`, `dimensions`, `stat`, `label`) or `markdown`, and optional `view`, `stacked`, `period`, `width`, `height`. | `list(object)` | `[]` | no |
| `sqs_queue_alarms` | Up to 25 queues: `queue_name`, `oldest_message_age_seconds` (>= 60), optional `visible_messages_threshold`. | `map(object)` | `{}` | no |
| `lambda_function_alarms` | Up to 25 functions: `function_name`, `errors_threshold`, `throttles_threshold` (>= 1). | `map(object)` | `{}` | no |
| `dynamodb_table_alarms` | Up to 25 tables: `table_name`, `throttle_events_threshold` (>= 1). | `map(object)` | `{}` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Alarm topic ARN. |
| `arn` | Alarm topic ARN. |
| `name` | Alarm topic name. |
| `sns_topic_arn` | Alarm topic ARN, for other modules' `alarm_actions`. |
| `dashboard_name` | Dashboard name, or null without widgets and alarms. |
| `dashboard_url` | Console URL of the dashboard, or null. |
| `alarm_arns` | Alarm ARNs keyed by `<kind>-<key>-<metric>`. |
| `kms_key_arn` | ARN of the KMS key encrypting the topic. |
| `publisher_policy_json` | IAM policy document for principals that publish to the topic. |
| `iacp_metadata` | `{ module = "monitoring", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_sns_topic`, `aws_sns_topic_policy`, `aws_sns_topic_subscription` (per
subscription), `aws_lambda_permission` (per `lambda` subscription), `aws_cloudwatch_metric_alarm` (per alarm),
`aws_cloudwatch_dashboard` (optional), and optional `aws_kms_key` +
`aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009):

```bash
docker compose up -d localstack
terraform -chdir=modules/monitoring init -backend=false
terraform -chdir=modules/monitoring test
cd tests && go test -run TestMonitoring ./...
```
