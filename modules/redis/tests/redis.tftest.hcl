# terraform test suite for modules/redis. LocalStack community does not
# emulate ElastiCache, so this suite needs no emulator at all (README, "Testing"):
#
#   - `command = plan` runs use the real AWS provider fully offline: dummy
#     credentials, no account lookup, every endpoint pointed at a closed local
#     port, and the account-reading data sources overridden below.
#   - `command = apply` runs use `mock_provider "aws"` (alias aws.mock) in
#     their own state, so they never call AWS. hashicorp/random runs locally.
#
#   terraform -chdir=modules/redis init -backend=false && terraform -chdir=modules/redis test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "mock"
  secret_key                  = "mock"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true

  # Nothing may leave the machine: any call would fail fast on a closed port.
  endpoints {
    cloudwatchlogs = "http://127.0.0.1:1"
    ec2            = "http://127.0.0.1:1"
    elasticache    = "http://127.0.0.1:1"
    iam            = "http://127.0.0.1:1"
    kms            = "http://127.0.0.1:1"
    secretsmanager = "http://127.0.0.1:1"
    sts            = "http://127.0.0.1:1"
  }
}

mock_provider "aws" {
  alias = "mock"

  # Generated strings are not valid JSON, which the provider's own validation
  # rejects. The rendered documents are asserted in the plan runs instead.
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_resource "aws_elasticache_replication_group" {
    defaults = {
      arn                      = "arn:aws:elasticache:us-east-1:123456789012:replicationgroup:platform-development-tftest-cache"
      primary_endpoint_address = "master.platform-development-tftest-cache.abc123.use1.cache.amazonaws.com"
      reader_endpoint_address  = "replica.platform-development-tftest-cache.abc123.use1.cache.amazonaws.com"
    }
  }

  mock_resource "aws_kms_key" {
    defaults = {
      arn    = "arn:aws:kms:us-east-1:123456789012:key/11111111-2222-3333-4444-555555555555"
      key_id = "11111111-2222-3333-4444-555555555555"
    }
  }

  mock_resource "aws_secretsmanager_secret" {
    defaults = {
      arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:iacp/redis/platform-development-tftest-cache/auth-token-AbCdEf"
    }
  }

  mock_resource "aws_security_group" {
    defaults = {
      id = "sg-0cache00000000000a"
    }
  }
}

override_data {
  target = data.aws_caller_identity.current
  values = {
    account_id = "123456789012"
  }
}

override_data {
  target = data.aws_partition.current
  values = {
    partition = "aws"
  }
}

override_data {
  target = data.aws_region.current
  values = {
    name = "us-east-1"
  }
}

variables {
  name        = "tftest-cache"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  vpc_id      = "vpc-0123456789abcdef0"
  subnet_ids  = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
  allowed_security_group_ids = [
    "sg-0aaaaaaaaaaaaaaa1",
  ]
}

run "development_defaults_apply" {
  command = apply

  # Separate state, so the offline plan runs never refresh mocked resources.
  state_key = "mock"

  providers = {
    aws = aws.mock
  }

  assert {
    condition     = aws_elasticache_replication_group.this.replication_group_id == "platform-development-tftest-cache" && aws_elasticache_replication_group.this.engine == "redis"
    error_message = "The replication group must be Redis named <team>-<environment>-<name>."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.at_rest_encryption_enabled == "true" && aws_elasticache_replication_group.this.kms_key_id == aws_kms_key.this[0].arn
    error_message = "Data at rest must be encrypted with the module-created customer-managed key."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation == true
    error_message = "The module-created key must rotate."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.transit_encryption_enabled == true && aws_elasticache_replication_group.this.transit_encryption_mode == "required"
    error_message = "TLS must be required in transit."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.auth_token == random_password.auth_token[0].result && length(random_password.auth_token[0].result) == 64
    error_message = "An AUTH token of 64 characters must protect the cache in token mode."
  }

  assert {
    condition     = aws_secretsmanager_secret.auth_token[0].kms_key_id == aws_kms_key.this[0].arn && aws_secretsmanager_secret_version.auth_token[0].secret_string == random_password.auth_token[0].result
    error_message = "The AUTH token must be stored in Secrets Manager under the module key."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.subnet_group_name == aws_elasticache_subnet_group.this.name && toset(aws_elasticache_subnet_group.this.subnet_ids) == toset(var.subnet_ids)
    error_message = "The cache must use the private subnet group built from subnet_ids."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.security_group_ids == toset([aws_security_group.this.id])
    error_message = "The cache must use only the module security group."
  }

  assert {
    condition = length(aws_vpc_security_group_ingress_rule.allowed) == 1 && alltrue([
      for r in aws_vpc_security_group_ingress_rule.allowed :
      r.referenced_security_group_id == "sg-0aaaaaaaaaaaaaaa1" && r.from_port == 6379 && r.to_port == 6379 && r.ip_protocol == "tcp" && r.cidr_ipv4 == null && r.cidr_ipv6 == null
    ])
    error_message = "Ingress must be TCP 6379 from the allowed security group only, never from CIDR ranges."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.parameter_group_name == aws_elasticache_parameter_group.this.name && aws_elasticache_parameter_group.this.family == "redis7"
    error_message = "The cache must use the module parameter group."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.num_cache_clusters == 1 && aws_elasticache_replication_group.this.automatic_failover_enabled == false
    error_message = "Development defaults to a single node without failover."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.snapshot_retention_limit == 7 && aws_elasticache_replication_group.this.final_snapshot_identifier == null
    error_message = "Development keeps 7 days of snapshots and takes no final snapshot."
  }

  assert {
    condition = toset([for c in aws_elasticache_replication_group.this.log_delivery_configuration : "${c.log_type}=${c.destination}"]) == toset([
      "slow-log=/aws/elasticache/platform-development-tftest-cache/slow-log",
      "engine-log=/aws/elasticache/platform-development-tftest-cache/engine-log",
    ])
    error_message = "Slow and engine logs must be delivered to the module log groups."
  }

  assert {
    condition     = alltrue([for g in aws_cloudwatch_log_group.this : g.kms_key_id == aws_kms_key.this[0].arn && g.retention_in_days == 30])
    error_message = "Log groups must be encrypted with the module key and keep 30 days."
  }

  assert {
    condition = alltrue([
      for r in concat(
        [aws_elasticache_replication_group.this, aws_elasticache_subnet_group.this, aws_security_group.this, aws_kms_key.this[0], aws_secretsmanager_secret.auth_token[0]],
        values(aws_cloudwatch_log_group.this),
        aws_vpc_security_group_ingress_rule.allowed,
      ) :
      r.tags["iacp:team"] == "platform" && r.tags["iacp:environment"] == "development" && r.tags["iacp:stack"] == "tftest" && r.tags["iacp:managed-by"] == "iacp" && r.tags["iacp:module"] == "redis"
    ])
    error_message = "Every taggable resource must carry the mandatory iacp:* tags."
  }

  assert {
    condition     = output.primary_endpoint == output.connection.host && output.port == 6379 && output.connection.secret_arn == output.secret_arn && output.secret_arn == aws_secretsmanager_secret.auth_token[0].arn
    error_message = "Outputs must expose the primary endpoint, port and the AUTH token secret ARN."
  }

  assert {
    condition     = output.iacp_metadata == { module = "redis", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "key_policy_scopes_logs" {
  command = plan

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.kms[0].json).Statement :
      s.Sid == "CloudWatchLogsForCacheLogGroups"
      && s.Principal.Service == "logs.us-east-1.amazonaws.com"
      && s.Condition.ArnLike["kms:EncryptionContext:aws:logs:arn"] == "arn:aws:logs:us-east-1:123456789012:log-group:/aws/elasticache/platform-development-tftest-cache/*"
    ])
    error_message = "The Logs service may use the key only for this cache's log groups."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.tags_all["iacp:stack"] == "tftest" && aws_elasticache_replication_group.this.tags_all["iacp:module"] == "redis"
    error_message = "Planned tags_all must carry the platform tags."
  }
}

run "production_defaults" {
  command = plan

  variables {
    environment = "production"
    kms_key_arn = "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
  }

  assert {
    condition     = aws_elasticache_replication_group.this.num_cache_clusters == 2
    error_message = "Production defaults to a primary and a replica."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.automatic_failover_enabled == true && aws_elasticache_replication_group.this.multi_az_enabled == true
    error_message = "Production caches fail over automatically across Availability Zones."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.final_snapshot_identifier == "platform-production-tftest-cache-final"
    error_message = "Production caches take a final snapshot."
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && aws_elasticache_replication_group.this.kms_key_id == var.kms_key_arn && aws_secretsmanager_secret.auth_token[0].kms_key_id == var.kms_key_arn
    error_message = "A supplied kms_key_arn replaces the module key everywhere."
  }

  assert {
    condition     = aws_secretsmanager_secret.auth_token[0].recovery_window_in_days == 7
    error_message = "Outside development the AUTH token secret keeps a recovery window."
  }
}

run "rbac_mode" {
  command = plan

  variables {
    auth_mode      = "rbac"
    user_group_ids = ["orders-app-users"]
  }

  assert {
    condition     = aws_elasticache_replication_group.this.user_group_ids == toset(["orders-app-users"]) && aws_elasticache_replication_group.this.auth_token == null
    error_message = "RBAC mode must attach the user group and set no AUTH token."
  }

  assert {
    condition     = length(random_password.auth_token) == 0 && length(aws_secretsmanager_secret.auth_token) == 0
    error_message = "RBAC mode must not create a token or its secret."
  }

  assert {
    condition     = aws_elasticache_replication_group.this.transit_encryption_enabled == true
    error_message = "RBAC requires and keeps TLS in transit."
  }
}

run "replicas_enable_failover" {
  command = plan

  variables {
    num_cache_clusters = 3
  }

  assert {
    condition     = aws_elasticache_replication_group.this.automatic_failover_enabled == true && aws_elasticache_replication_group.this.num_cache_clusters == 3
    error_message = "Two or more nodes must enable automatic failover."
  }
}

run "rejects_single_node_in_production" {
  command = plan

  variables {
    environment        = "production"
    num_cache_clusters = 1
  }

  expect_failures = [var.num_cache_clusters]
}

run "rejects_no_snapshots_in_production" {
  command = plan

  variables {
    environment             = "production"
    snapshot_retention_days = 0
  }

  expect_failures = [var.snapshot_retention_days]
}

run "rejects_rbac_without_user_group" {
  command = plan

  variables {
    auth_mode = "rbac"
  }

  expect_failures = [var.user_group_ids]
}

run "rejects_user_group_in_token_mode" {
  command = plan

  variables {
    user_group_ids = ["orders-app-users"]
  }

  expect_failures = [var.user_group_ids]
}

run "rejects_unknown_auth_mode" {
  command = plan

  variables {
    auth_mode = "none"
  }

  expect_failures = [var.auth_mode]
}

run "rejects_cidr_instead_of_security_group" {
  command = plan

  variables {
    allowed_security_group_ids = ["10.0.0.0/8"]
  }

  expect_failures = [var.allowed_security_group_ids]
}

run "rejects_too_long_group_id" {
  command = plan

  variables {
    name = "a-very-long-cache-name-for-orders"
  }

  expect_failures = [var.name]
}

run "rejects_unsupported_engine_version" {
  command = plan

  variables {
    engine_version = "6.2"
  }

  expect_failures = [var.engine_version]
}
