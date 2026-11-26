# modules/postgres

Private RDS PostgreSQL instance with storage encrypted by a customer-managed
key, TLS enforced, an RDS-managed master password, backups and
environment-driven protection. Part of the iacp module registry (see
[docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "orders_db" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/postgres?ref=modules/postgres/v1.0.0"

  name        = "orders-db"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  # Wired from the outputs of the vpc and ecs-service stacks.
  vpc_id                     = "vpc-0123456789abcdef0"
  subnet_ids                 = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
  allowed_security_group_ids = ["sg-0aaaaaaaaaaaaaaa1"]

  engine_version = "16"
  instance_class = "db.t4g.micro"
  database_name  = "orders"
}
```

Applications connect to `endpoint`:`port` with TLS and read the master
credentials from the Secrets Manager secret `secret_arn` (the ecs-service
module injects it through its `secrets` input). Give the application's
security group to `allowed_security_group_ids` and, on the application side,
allow egress to this module's `security_group_id`.

The module declares no provider. The calling root configures `provider "aws"`.
It also uses `hashicorp/random` (local only) for the final snapshot suffix.
[`examples/complete`](examples/complete) plans the module with a stand-in
network.

## Secure defaults

| Control | Implementation |
|---|---|
| Encryption at rest | `storage_encrypted = true` with a customer-managed KMS key (module-created with rotation unless `kms_key_arn` is set). The same key encrypts Performance Insights, the exported logs and the master secret. |
| Encryption in transit | Module parameter group with `rds.force_ssl = 1`: connections without TLS are rejected. |
| Credentials | `manage_master_user_password = true`: RDS generates and rotates the master password in Secrets Manager (encrypted with the module key). No password input, no password output, nothing in Terraform state. IAM database authentication is on by default. |
| Network | `publicly_accessible = false`, a DB subnet group over the given private subnets, and a security group whose only rules admit the database port from `allowed_security_group_ids`. No CIDR ingress and no egress. |
| Recovery | Automated backups for 7 days (35 in production; at least 7 in production), `copy_tags_to_snapshot`. Outside development, deletion protection is on and a final snapshot `<name>-final-<suffix>` is taken; development stays disposable so TTL destroys work. The 8-character suffix comes from a `random_id` kept in state: stable for the life of the instance, new for a re-created one (and when the identifier changes), so a second destroy never collides with an earlier snapshot. |
| Availability | Multi-AZ by default in production, and `multi_az = false` is rejected there. |
| Observability | Performance Insights (encrypted, 7 days); `postgresql` and `upgrade` logs exported to log groups created up front with the module key and a retention (default 30 days); connection, disconnection, DDL and slow-statement (> 1 s) logging. |
| Maintenance | Automatic minor version upgrades in the maintenance window; major upgrades are never applied implicitly; changes wait for the window (`apply_immediately = false`). |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last, on every taggable resource. |

Enhanced Monitoring (which needs a monitoring role) and default CloudWatch
alarms are planned for a later minor release.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical name. The instance identifier is `<team>-<environment>-<name>` (at most 63 characters, no consecutive hyphens). | `string` | n/a | yes |
| `team` | Owning team slug, without trailing or consecutive hyphens. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `vpc_id` | VPC that holds the database. | `string` | n/a | yes |
| `subnet_ids` | 2-16 private subnets in at least two Availability Zones. | `list(string)` | n/a | yes |
| `allowed_security_group_ids` | Security groups allowed on the database port (at most 20). | `list(string)` | `[]` | no |
| `port` | Database port (1150-65535). | `number` | `5432` | no |
| `engine_version` | PostgreSQL major version 14-17, optionally with a minor version. | `string` | `"16"` | no |
| `instance_class` | RDS instance class. | `string` | `"db.t4g.micro"` | no |
| `allocated_storage_gb` | Initial gp3 storage (20-65536 GiB). | `number` | `20` | no |
| `max_allocated_storage_gb` | Storage autoscaling ceiling (at least `allocated_storage_gb`). Null disables autoscaling. | `number` | `null` | no |
| `multi_az` | Standby in a second AZ. Null means on in production only; `false` is rejected in production. | `bool` | `null` | no |
| `database_name` | Database created with the instance. Null creates none. | `string` | `null` | no |
| `master_username` | Master user name (not `postgres`, `rdsadmin`, `admin` or `root`). | `string` | `"iacp_admin"` | no |
| `backup_retention_days` | Backup retention (1-35). Null means 7, or 35 in production; production needs at least 7. | `number` | `null` | no |
| `iam_database_authentication_enabled` | Allow IAM database authentication. | `bool` | `true` | no |
| `log_retention_days` | CloudWatch Logs retention for exported logs. | `number` | `30` | no |
| `kms_key_arn` | Existing customer-managed KMS key ARN. If null, a dedicated key is created. Must be known at plan time. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | DB instance identifier. |
| `arn` | DB instance ARN. |
| `name` | Computed identifier (`<team>-<environment>-<name>`). |
| `endpoint` | Host name clients connect to. |
| `port` | Database port. |
| `secret_arn` | ARN of the RDS-managed master user secret. The password is never an output. |
| `connection` | `{ host, port, secret_arn }`. |
| `security_group_id` | Security group attached to the instance. |
| `kms_key_arn` | KMS key encrypting storage, Performance Insights, logs and the secret. |
| `iacp_metadata` | `{ module = "postgres", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_db_instance`, `aws_db_subnet_group`, `aws_db_parameter_group`,
`aws_security_group` with one `aws_vpc_security_group_ingress_rule` per allowed
group, two `aws_cloudwatch_log_group`s, `random_id` (outside development), and optional `aws_kms_key` +
`aws_kms_alias`.

## Testing

LocalStack community (the pinned 4.4 image) does not emulate RDS, so this
module is tested without an emulator:

- [`tests/postgres.tftest.hcl`](tests/postgres.tftest.hcl) runs `plan`
  assertions with the real AWS provider fully offline (dummy credentials, no
  account lookup, every endpoint pointed at a closed local port, the
  account-reading data sources replaced by `override_data`), so provider
  validation and the rendered key policy are real. One `apply` run uses
  `mock_provider "aws"` in its own state to assert the applied attributes:
  encryption with the module key, `publicly_accessible = false`,
  `rds.force_ssl = 1`, security-group-only ingress, the managed master secret,
  protection by environment, tags and outputs. Validation runs check that
  unsafe inputs (single-AZ or short backups in production, CIDRs instead of
  security groups, reserved user names) are rejected.
- [`examples/complete`](examples/complete) is planned, never applied, and
  checked with conftest against `policies/opa` (`policies/example-plan.sh`).

```bash
terraform -chdir=modules/postgres init -backend=false
terraform -chdir=modules/postgres test       # offline, no LocalStack needed
make up && policies/example-plan.sh modules/postgres
```

In CI the suite runs in the `module-tests-offline` job, and the example plan
in the `module-tests` job (its LocalStack container only answers STS).
