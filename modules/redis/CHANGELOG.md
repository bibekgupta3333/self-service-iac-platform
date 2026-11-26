# Changelog — modules/redis

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/redis/vX.Y.Z`.

## [1.0.0] - 2026-11-26

### Added
- ElastiCache for Redis OSS (7.0/7.1) replication group named
  `<team>-<environment>-<name>`, in a subnet group over the given private
  subnets, with a module parameter group (`maxmemory_policy` input).
- Security group with no CIDR ingress and no egress; one ingress rule per
  `allowed_security_group_ids` entry on the cache port.
- Encryption at rest with a customer-managed KMS key (module-created with
  rotation and a Logs grant scoped to the cache's log groups, unless
  `kms_key_arn` is given) and TLS required in transit.
- `auth_mode = "token"`: a random 64-character AUTH token stored in Secrets
  Manager under the module key (never an output). `auth_mode = "rbac"`: one
  RBAC user group from `user_group_ids`.
- Automatic failover and Multi-AZ with 2+ nodes; 2 nodes by default and at
  least 2 required in production.
- Daily snapshots (7 days, at least 1 in production) and a final snapshot
  outside development.
- Slow and engine logs delivered to KMS-encrypted log groups with retention.
- Outputs `primary_endpoint`, `reader_endpoint`, `port`, `secret_arn`,
  `connection`, `security_group_id`.
- Mandatory `iacp:*` tags on every taggable resource.
- Offline `terraform test` suite (real provider for plans, `mock_provider`
  for applies), because LocalStack community has no ElastiCache.
