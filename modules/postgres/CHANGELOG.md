# Changelog — modules/postgres

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/postgres/vX.Y.Z`.

## [1.0.0] - 2026-11-26

### Added
- RDS PostgreSQL instance (14-17, gp3) named `<team>-<environment>-<name>`,
  in a DB subnet group over the given private subnets, never publicly
  accessible.
- Security group with no CIDR ingress and no egress; one ingress rule per
  `allowed_security_group_ids` entry on the database port.
- Storage, Performance Insights, exported logs and the master secret
  encrypted with a customer-managed KMS key (module-created with rotation and
  a Logs grant scoped to the instance's log groups, unless `kms_key_arn` is
  given).
- Parameter group with `rds.force_ssl = 1` plus connection, DDL and
  slow-statement logging.
- `manage_master_user_password`: RDS generates and rotates the master password
  in Secrets Manager; the password never appears in a variable, an output or
  state. IAM database authentication on by default.
- Backups (7 days, 35 in production, at least 7 in production), deletion
  protection and a final snapshot outside development, Multi-AZ by default and
  required in production. The final snapshot is named
  `<name>-final-<suffix>`, with a `random_id` suffix kept in state, so
  re-created instances never collide with an earlier snapshot (adds
  `hashicorp/random` >= 3.6, < 4.0).
- `postgresql` and `upgrade` log groups created up front with retention and
  KMS.
- Outputs `endpoint`, `port`, `secret_arn`, `connection`, `security_group_id`.
- Mandatory `iacp:*` tags on every taggable resource.
- `team` and `name` reject trailing and consecutive hyphens, which would make
  `<team>-<environment>-<name>` an invalid identifier.
- Offline `terraform test` suite (real provider for plans, `mock_provider`
  for applies), because LocalStack community has no RDS.
