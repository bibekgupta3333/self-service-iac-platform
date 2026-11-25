# modules/s3

Private, KMS-encrypted S3 bucket with secure defaults. Part of the iacp module
registry (see [docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "artifacts" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/s3?ref=modules/s3/v1.0.0"

  name        = "artifacts"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform
}
```

The module declares no provider. The calling root configures `provider "aws"`.
[`examples/complete`](examples/complete) shows a LocalStack configuration.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | SSE-KMS with bucket keys. A dedicated key with rotation is created unless `kms_key_arn` is set. |
| Encryption in transit | Bucket policy denies any request with `aws:SecureTransport = false`. |
| No public access | All four public access block flags on. `BucketOwnerEnforced` disables ACLs. |
| Recoverability | Versioning on (forced in production). Noncurrent versions expire after `noncurrent_version_expiration_days`. |
| Least privilege | No roles are created. Consumers attach `read_policy_json` or `write_policy_json`. |
| Safe deletion | `force_destroy` defaults to `false` and is rejected in production. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last, so user tags cannot override them. |

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical bucket name. The final name is `<team>-<environment>-<name>` (at most 63 characters). | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `versioning_enabled` | Keep object versions. Always enabled in production. | `bool` | `true` | no |
| `kms_key_arn` | Existing KMS key ARN. If null, a dedicated key is created. Aliases are not accepted. | `string` | `null` | no |
| `noncurrent_version_expiration_days` | Days after which noncurrent versions expire (1-3650). | `number` | `90` | no |
| `transition_to_ia_days` | Days after which current objects move to STANDARD_IA (>= 30). Null disables. | `number` | `null` | no |
| `force_destroy` | Allow destroy while objects exist. Not allowed in production. | `bool` | `false` | no |
| `access_log_bucket` | Existing bucket for server access logs. Null disables access logging. The target bucket must allow log delivery. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Bucket name. |
| `arn` | Bucket ARN. |
| `name` | Computed bucket name (`<team>-<environment>-<name>`). |
| `kms_key_arn` | ARN of the KMS key encrypting the bucket. |
| `read_policy_json` | IAM policy document granting read-only access to the bucket and its key. |
| `write_policy_json` | IAM policy document granting read/write access to the bucket and its key. |
| `iacp_metadata` | `{ module = "s3", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_s3_bucket`, `aws_s3_bucket_public_access_block`, `aws_s3_bucket_ownership_controls`,
`aws_s3_bucket_versioning`, `aws_s3_bucket_server_side_encryption_configuration`,
`aws_s3_bucket_lifecycle_configuration`, `aws_s3_bucket_policy`, optional
`aws_s3_bucket_logging`, and optional `aws_kms_key` + `aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009):

```bash
docker compose up -d localstack
terraform -chdir=modules/s3 init -backend=false
terraform -chdir=modules/s3 test
cd tests && go test -run TestS3 ./...
```
