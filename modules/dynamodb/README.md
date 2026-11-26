# modules/dynamodb

DynamoDB table encrypted with a customer managed key, with point-in-time
recovery always on and deletion protection outside development. Part of the
iacp module registry (see [docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "orders" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/dynamodb?ref=modules/dynamodb/v1.0.0"

  name        = "orders"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  hash_key  = "pk"
  range_key = "sk"

  global_secondary_indexes = [
    { name = "by-status", hash_key = "status", range_key = "created_at", range_key_type = "N" },
  ]

  ttl_attribute_name = "expires_at"
}
```

The module declares no provider. The calling root configures `provider "aws"`.
[`examples/complete`](examples/complete) shows a LocalStack configuration.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | SSE with a customer managed key. A dedicated key with rotation is created unless `kms_key_arn` is set. The AWS owned and `aws/dynamodb` keys are never used. |
| Recoverable | Point-in-time recovery is always on (not an input). Deletion protection is on outside development and cannot be turned off there. |
| Cost | On-demand billing (`PAY_PER_REQUEST`) by default. `PROVISIONED` needs explicit `read_capacity` and `write_capacity`; capacity auto scaling is not supported in v1.0.0 (see below). |
| Least privilege | No roles or resource policies. Callers attach `read_policy_json` / `write_policy_json`, whose KMS grants only apply through DynamoDB (`kms:ViaService`). Actions are explicit, so the documents pass the lambda module's `extra_policy_jsons` guard. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last on the table and the key. |

Attribute definitions are derived from the key attributes of the table and its
indexes. Non-key attributes need no definition in DynamoDB. A key attribute
shared between the table and an index, or between indexes, must use the same
type everywhere, which `variables.tf` checks.

## Provisioned capacity and auto scaling

The module does not create Application Auto Scaling targets or policies, and
it does not ignore capacity changes, because `ignore_changes` cannot depend on
an input. With `PROVISIONED` billing the table (and each index) keeps exactly
`read_capacity` / `write_capacity`; if something else scales the table, the
next apply resets it. Use on-demand billing for variable load, or change the
capacity inputs. Auto scaling may come in a later minor release.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical table name. The final name is `<team>-<environment>-<name>`. | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `hash_key` | Partition key attribute name. | `string` | n/a | yes |
| `hash_key_type` | Partition key type: `S`, `N` or `B`. | `string` | `"S"` | no |
| `range_key` | Optional sort key attribute name. Must differ from `hash_key`. | `string` | `null` | no |
| `range_key_type` | Sort key type: `S`, `N` or `B`. | `string` | `"S"` | no |
| `billing_mode` | `PAY_PER_REQUEST` or `PROVISIONED`. | `string` | `"PAY_PER_REQUEST"` | no |
| `read_capacity` | Read capacity units (1-40000). Required with `PROVISIONED`, not allowed otherwise. | `number` | `null` | no |
| `write_capacity` | Write capacity units (1-40000). Required with `PROVISIONED`, not allowed otherwise. | `number` | `null` | no |
| `table_class` | `STANDARD` or `STANDARD_INFREQUENT_ACCESS`. | `string` | `"STANDARD"` | no |
| `global_secondary_indexes` | Up to 20 indexes: `name`, `hash_key`, optional `hash_key_type`, `range_key`, `range_key_type`, `projection_type` (`ALL`, `KEYS_ONLY`, `INCLUDE`), `non_key_attributes` (only with `INCLUDE`), and `read_capacity`/`write_capacity` (only with `PROVISIONED`, default the table's). | `list(object)` | `[]` | no |
| `ttl_attribute_name` | Attribute holding an expiry time in epoch seconds. Null leaves TTL off. | `string` | `null` | no |
| `stream_view_type` | `KEYS_ONLY`, `NEW_IMAGE`, `OLD_IMAGE` or `NEW_AND_OLD_IMAGES`. Null leaves streams off. | `string` | `null` | no |
| `deletion_protection_enabled` | Null turns it on outside development. `false` is rejected outside development. | `bool` | `null` | no |
| `kms_key_arn` | Existing KMS key ARN. If null, a dedicated key is created. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Table name. |
| `arn` | Table ARN. |
| `name` | Table name (`<team>-<environment>-<name>`). |
| `stream_arn` | Stream ARN, or null when streams are off. |
| `kms_key_arn` | ARN of the KMS key encrypting the table. |
| `read_policy_json` | IAM policy document for readers (get, query, scan on the table and its indexes; decrypt through DynamoDB). With a stream, it also grants `DescribeStream`, `GetRecords` and `GetShardIterator` on the stream and `ListStreams`, which is what a Lambda event source mapping needs. |
| `write_policy_json` | IAM policy document for writers (put, update, delete, batch write; encrypt and decrypt through DynamoDB). |
| `iacp_metadata` | `{ module = "dynamodb", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_dynamodb_table`, and optional `aws_kms_key` + `aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009):

```bash
docker compose up -d localstack
terraform -chdir=modules/dynamodb init -backend=false
terraform -chdir=modules/dynamodb test
cd tests && go test -run TestDynamoDB ./...
```
