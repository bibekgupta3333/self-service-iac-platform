# modules/redis

Private ElastiCache for Redis OSS replication group, encrypted at rest with a
customer-managed key, TLS required in transit, authenticated with an AUTH
token kept in Secrets Manager or with RBAC user groups, and failing over
automatically in production. Part of the iacp module registry (see
[docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "sessions" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/redis?ref=modules/redis/v1.0.0"

  name        = "sessions"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  # Wired from the outputs of the vpc and ecs-service stacks.
  vpc_id                     = "vpc-0123456789abcdef0"
  subnet_ids                 = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
  allowed_security_group_ids = ["sg-0aaaaaaaaaaaaaaa1"]

  node_type = "cache.t4g.micro"
}
```

Clients connect to `primary_endpoint`:`port` with TLS. In `auth_mode =
"token"` (the default) they read the AUTH token from `secret_arn` (the
ecs-service module injects it through its `secrets` input). In `auth_mode =
"rbac"` pass one ElastiCache user group in `user_group_ids`; its users and
their passwords (in Secrets Manager) are managed outside this module, and
`secret_arn` is null.

The module declares no provider. The calling root configures `provider "aws"`.
It also uses `hashicorp/random` (local only) to generate the AUTH token.
[`examples/complete`](examples/complete) plans the module with a stand-in
network.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | `at_rest_encryption_enabled = true` with a customer-managed KMS key (module-created with rotation unless `kms_key_arn` is set). The same key encrypts the logs and the AUTH token secret. |
| Encryption in transit | `transit_encryption_enabled = true` with `transit_encryption_mode = "required"`: plaintext connections are refused. |
| Authentication | `token`: a 64-character random AUTH token stored in a Secrets Manager secret under the module key; never an output. `rbac`: exactly one RBAC user group. One of the two is always active. |
| Network | A subnet group over the given private subnets and a security group whose only rules admit the cache port from `allowed_security_group_ids`. No CIDR ingress and no egress. |
| Availability | 2 nodes by default in production (fewer is rejected), with automatic failover and Multi-AZ whenever there are 2 or more nodes. |
| Recovery | Daily snapshots kept 7 days (production must keep at least 1). Outside development a final snapshot `<name>-final-<suffix>` is taken; the suffix comes from a `random_id` kept in state (stable for the life of the cache, new for a re-created one), so a second destroy never collides with an earlier snapshot. |
| Observability | Slow and engine logs delivered as JSON to log groups with the module key and a retention (default 30 days). The module key grants CloudWatch Logs (`logs.<region>`, scoped to these log groups) and the log delivery service (`delivery.logs.amazonaws.com`, scoped to this account with `aws:SourceAccount` and `aws:SourceArn`). |
| Maintenance | Module parameter group (`redis7`, configurable `maxmemory-policy`), automatic minor version upgrades in the maintenance window, no immediate applies. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last, on every taggable resource. |

The AUTH token is a resource argument, so it is also stored in Terraform
state, which the platform keeps encrypted and access-controlled (docs/02).
Use `rbac` to keep credentials out of state entirely. Scheduled token
rotation is planned for a later minor release.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical name. The replication group ID is `<team>-<environment>-<name>` (at most 40 characters, no consecutive hyphens). | `string` | n/a | yes |
| `team` | Owning team slug, without trailing or consecutive hyphens. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `vpc_id` | VPC that holds the cache. | `string` | n/a | yes |
| `subnet_ids` | 2-16 private subnets in at least two Availability Zones. | `list(string)` | n/a | yes |
| `allowed_security_group_ids` | Security groups allowed on the cache port (at most 20). | `list(string)` | `[]` | no |
| `port` | Cache port (1024-65535). | `number` | `6379` | no |
| `engine_version` | Redis OSS `7.0` or `7.1`. | `string` | `"7.1"` | no |
| `node_type` | Cache node type. | `string` | `"cache.t4g.micro"` | no |
| `num_cache_clusters` | Nodes (1-6). Null means 2 in production and 1 elsewhere; production needs at least 2. | `number` | `null` | no |
| `maxmemory_policy` | Redis eviction policy. | `string` | `"volatile-lru"` | no |
| `auth_mode` | `token` (AUTH token in Secrets Manager) or `rbac`. | `string` | `"token"` | no |
| `user_group_ids` | Exactly one RBAC user group with `rbac`, none with `token`. | `list(string)` | `[]` | no |
| `snapshot_retention_days` | Snapshot retention (0-35); production needs at least 1. | `number` | `7` | no |
| `log_retention_days` | CloudWatch Logs retention for slow and engine logs. | `number` | `30` | no |
| `kms_key_arn` | Existing customer-managed KMS key ARN. If null, a dedicated key is created. Must be known at plan time. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | Replication group ID. |
| `arn` | Replication group ARN. |
| `name` | Computed ID (`<team>-<environment>-<name>`). |
| `primary_endpoint` | Primary endpoint host name (reads and writes, TLS). |
| `reader_endpoint` | Reader endpoint host name (TLS). |
| `port` | Cache port. |
| `secret_arn` | ARN of the AUTH token secret, or null with RBAC. The token is never an output. |
| `connection` | `{ host, port, secret_arn }`. |
| `security_group_id` | Security group attached to the cache. |
| `kms_key_arn` | KMS key encrypting data at rest, logs and the secret. |
| `iacp_metadata` | `{ module = "redis", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_elasticache_replication_group`, `aws_elasticache_subnet_group`,
`aws_elasticache_parameter_group`, `aws_security_group` with one
`aws_vpc_security_group_ingress_rule` per allowed group, two
`aws_cloudwatch_log_group`s, optional `aws_kms_key` + `aws_kms_alias`, and in
token mode `random_password` + `aws_secretsmanager_secret` (+ version), and
outside development `random_id` for the final snapshot suffix.

## Testing

LocalStack community (the pinned 4.4 image) does not emulate ElastiCache, so
this module is tested without an emulator:

- [`tests/redis.tftest.hcl`](tests/redis.tftest.hcl) runs `plan` assertions
  with the real AWS provider fully offline (dummy credentials, no account
  lookup, every endpoint pointed at a closed local port, the account-reading
  data sources replaced by `override_data`). One `apply` run uses
  `mock_provider "aws"` in its own state to assert the applied attributes:
  at-rest encryption with the module key, required TLS, the AUTH token and its
  secret, security-group-only ingress, log delivery, tags and outputs. Plan
  runs cover production failover and final snapshots, RBAC mode and a supplied
  key; validation runs check that unsafe inputs are rejected.
- [`examples/complete`](examples/complete) is planned, never applied, and
  checked with conftest against `policies/opa` (`policies/example-plan.sh`).

```bash
terraform -chdir=modules/redis init -backend=false
terraform -chdir=modules/redis test          # offline, no LocalStack needed
make up && policies/example-plan.sh modules/redis
```

In CI the suite runs in the `module-tests-offline` job, and the example plan
in the `module-tests` job (its LocalStack container only answers STS).
