# terraform test suite for modules/postgres. LocalStack community does not
# emulate RDS, so this suite needs no emulator at all (README, "Testing"):
#
#   - `command = plan` runs use the real AWS provider fully offline: dummy
#     credentials, no account lookup, every endpoint pointed at a closed local
#     port, and the account-reading data sources overridden below. Planning
#     new resources makes no AWS calls, so the real provider schema, its
#     validation and the rendered IAM documents are all exercised.
#   - `command = apply` runs use `mock_provider "aws"` (alias aws.mock), which
#     returns generated values for computed attributes and never calls AWS.
#
#   terraform -chdir=modules/postgres init -backend=false && terraform -chdir=modules/postgres test

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
    iam            = "http://127.0.0.1:1"
    kms            = "http://127.0.0.1:1"
    rds            = "http://127.0.0.1:1"
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

  mock_resource "aws_db_instance" {
    defaults = {
      address = "platform-development-tftest-db.abc123.us-east-1.rds.amazonaws.com"
      arn     = "arn:aws:rds:us-east-1:123456789012:db:platform-development-tftest-db"
      master_user_secret = [{
        secret_arn    = "arn:aws:secretsmanager:us-east-1:123456789012:secret:rds!db-tftest-AbCdEf"
        secret_status = "active"
        kms_key_id    = "arn:aws:kms:us-east-1:123456789012:key/11111111-2222-3333-4444-555555555555"
      }]
    }
  }

  mock_resource "aws_kms_key" {
    defaults = {
      arn    = "arn:aws:kms:us-east-1:123456789012:key/11111111-2222-3333-4444-555555555555"
      key_id = "11111111-2222-3333-4444-555555555555"
    }
  }

  mock_resource "aws_security_group" {
    defaults = {
      id = "sg-0db0000000000000a"
    }
  }
}

# The only data sources that would read the account; fixed values keep the
# rendered ARNs deterministic for both providers.
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
  name        = "tftest-db"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  vpc_id      = "vpc-0123456789abcdef0"
  subnet_ids  = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
  allowed_security_group_ids = [
    "sg-0aaaaaaaaaaaaaaa1",
    "sg-0bbbbbbbbbbbbbbb2",
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
    condition     = aws_db_instance.this.identifier == "platform-development-tftest-db" && aws_db_instance.this.engine == "postgres"
    error_message = "The instance must be PostgreSQL named <team>-<environment>-<name>."
  }

  assert {
    condition     = aws_db_instance.this.storage_encrypted == true && aws_db_instance.this.kms_key_id == aws_kms_key.this[0].arn
    error_message = "Storage must be encrypted with the module-created customer-managed key."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation == true
    error_message = "The module-created key must rotate."
  }

  assert {
    condition     = aws_db_instance.this.publicly_accessible == false
    error_message = "The instance must never be publicly accessible."
  }

  assert {
    condition     = aws_db_instance.this.manage_master_user_password == true && aws_db_instance.this.master_user_secret_kms_key_id == aws_kms_key.this[0].arn
    error_message = "The master password must be managed by RDS in Secrets Manager with the module key."
  }

  assert {
    condition     = aws_db_instance.this.password == null
    error_message = "No master password may pass through Terraform."
  }

  assert {
    condition     = aws_db_instance.this.performance_insights_enabled == true && aws_db_instance.this.performance_insights_kms_key_id == aws_kms_key.this[0].arn
    error_message = "Performance Insights must be on and encrypted with the module key."
  }

  assert {
    condition     = aws_db_instance.this.db_subnet_group_name == aws_db_subnet_group.this.name && toset(aws_db_subnet_group.this.subnet_ids) == toset(var.subnet_ids)
    error_message = "The instance must use the private subnet group built from subnet_ids."
  }

  assert {
    condition     = aws_db_instance.this.vpc_security_group_ids == toset([aws_security_group.this.id])
    error_message = "The instance must use only the module security group."
  }

  assert {
    condition     = tolist([for r in aws_vpc_security_group_ingress_rule.allowed : r.referenced_security_group_id]) == var.allowed_security_group_ids
    error_message = "There must be one ingress rule per allowed security group."
  }

  assert {
    condition = alltrue([
      for r in aws_vpc_security_group_ingress_rule.allowed :
      r.from_port == 5432 && r.to_port == 5432 && r.ip_protocol == "tcp" && r.cidr_ipv4 == null && r.cidr_ipv6 == null
    ])
    error_message = "Ingress must be TCP 5432 from security groups only, never from CIDR ranges."
  }

  assert {
    condition     = contains([for p in aws_db_parameter_group.this.parameter : "${p.name}=${p.value}"], "rds.force_ssl=1")
    error_message = "The parameter group must set rds.force_ssl=1."
  }

  assert {
    condition     = aws_db_instance.this.parameter_group_name == aws_db_parameter_group.this.name && aws_db_parameter_group.this.family == "postgres16"
    error_message = "The instance must use the module parameter group for its engine family."
  }

  assert {
    condition     = aws_db_instance.this.deletion_protection == false && aws_db_instance.this.skip_final_snapshot == true
    error_message = "Development databases stay disposable: no deletion protection, no final snapshot."
  }

  assert {
    condition     = aws_db_instance.this.multi_az == false && aws_db_instance.this.backup_retention_period == 7
    error_message = "Development defaults are single-AZ with 7 days of backups."
  }

  assert {
    condition     = alltrue([for g in aws_cloudwatch_log_group.this : g.kms_key_id == aws_kms_key.this[0].arn && g.retention_in_days == 30])
    error_message = "Exported log groups must be encrypted with the module key and keep 30 days."
  }

  assert {
    condition = alltrue([
      for r in concat(
        [aws_db_instance.this, aws_db_subnet_group.this, aws_security_group.this, aws_db_parameter_group.this, aws_kms_key.this[0]],
        values(aws_cloudwatch_log_group.this),
        aws_vpc_security_group_ingress_rule.allowed,
      ) :
      r.tags["iacp:team"] == "platform" && r.tags["iacp:environment"] == "development" && r.tags["iacp:stack"] == "tftest" && r.tags["iacp:managed-by"] == "iacp" && r.tags["iacp:module"] == "postgres"
    ])
    error_message = "Every taggable resource must carry the mandatory iacp:* tags."
  }

  assert {
    condition     = output.connection.secret_arn == output.secret_arn && output.connection.port == 5432 && output.endpoint == output.connection.host
    error_message = "connection must expose host, port and the secret ARN."
  }

  assert {
    condition     = startswith(output.secret_arn, "arn:aws:secretsmanager:")
    error_message = "secret_arn must be the RDS-managed Secrets Manager secret."
  }

  assert {
    condition     = output.iacp_metadata == { module = "postgres", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "key_policy_scopes_logs" {
  command = plan

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.kms[0].json).Statement :
      s.Sid == "CloudWatchLogsForInstanceLogGroups"
      && s.Principal.Service == "logs.us-east-1.amazonaws.com"
      && s.Condition.ArnLike["kms:EncryptionContext:aws:logs:arn"] == "arn:aws:logs:us-east-1:123456789012:log-group:/aws/rds/instance/platform-development-tftest-db/*"
    ])
    error_message = "The Logs service may use the key only for this instance's log groups."
  }

  assert {
    condition     = length(jsondecode(data.aws_iam_policy_document.kms[0].json).Statement) == 2
    error_message = "The key policy must contain only account administration and the scoped Logs grant."
  }

  assert {
    condition     = aws_db_instance.this.tags_all["iacp:stack"] == "tftest" && aws_db_instance.this.tags_all["iacp:module"] == "postgres"
    error_message = "Planned tags_all must carry the platform tags."
  }
}

run "user_tags_cannot_override_platform_tags" {
  command = plan

  variables {
    tags = { "cost-center" = "cc-1234", "Name" = "custom" }
  }

  assert {
    condition     = aws_db_instance.this.tags["cost-center"] == "cc-1234" && aws_db_instance.this.tags["iacp:team"] == "platform"
    error_message = "User tags are merged under the platform tags."
  }
}

run "staging_is_protected" {
  command = plan

  variables {
    environment = "staging"
    kms_key_arn = "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
  }

  assert {
    condition     = aws_db_instance.this.deletion_protection == true && aws_db_instance.this.skip_final_snapshot == false
    error_message = "Outside development, deletion protection is on and a final snapshot is taken."
  }

  assert {
    condition     = aws_db_instance.this.final_snapshot_identifier == "platform-staging-tftest-db-final"
    error_message = "The final snapshot must be named after the instance."
  }

  assert {
    condition     = aws_db_instance.this.multi_az == false && aws_db_instance.this.backup_retention_period == 7
    error_message = "Staging defaults are single-AZ with 7 days of backups."
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && aws_db_instance.this.kms_key_id == var.kms_key_arn && aws_db_instance.this.master_user_secret_kms_key_id == var.kms_key_arn
    error_message = "A supplied kms_key_arn replaces the module key everywhere."
  }
}

run "production_defaults" {
  command = plan

  variables {
    environment = "production"
    kms_key_arn = "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
  }

  assert {
    condition     = aws_db_instance.this.multi_az == true
    error_message = "Production databases are Multi-AZ by default."
  }

  assert {
    condition     = aws_db_instance.this.backup_retention_period == 35
    error_message = "Production keeps 35 days of backups by default."
  }

  assert {
    condition     = aws_db_instance.this.deletion_protection == true && aws_db_instance.this.skip_final_snapshot == false && aws_db_instance.this.delete_automated_backups == false
    error_message = "Production databases are protected and keep a final snapshot and their backups."
  }

  assert {
    condition     = aws_db_instance.this.publicly_accessible == false && aws_db_instance.this.storage_encrypted == true
    error_message = "Production databases are private and encrypted."
  }
}

run "custom_engine_and_port" {
  command = plan

  variables {
    engine_version = "15.7"
    port           = 6543
  }

  assert {
    condition     = aws_db_parameter_group.this.family == "postgres15" && aws_db_parameter_group.this.name == "platform-development-tftest-db-postgres15"
    error_message = "The parameter group family must follow the engine major version."
  }

  assert {
    condition     = alltrue([for r in aws_vpc_security_group_ingress_rule.allowed : r.from_port == 6543 && r.to_port == 6543])
    error_message = "Ingress rules must follow the configured port."
  }
}

run "no_allowed_groups_means_no_ingress" {
  command = plan

  variables {
    allowed_security_group_ids = []
  }

  assert {
    condition     = length(aws_vpc_security_group_ingress_rule.allowed) == 0
    error_message = "Without allowed security groups nothing can reach the database."
  }
}

run "rejects_single_az_in_production" {
  command = plan

  variables {
    environment = "production"
    multi_az    = false
  }

  expect_failures = [var.multi_az]
}

run "rejects_short_backups_in_production" {
  command = plan

  variables {
    environment           = "production"
    backup_retention_days = 3
  }

  expect_failures = [var.backup_retention_days]
}

run "rejects_out_of_range_backups" {
  command = plan

  variables {
    backup_retention_days = 0
  }

  expect_failures = [var.backup_retention_days]
}

run "rejects_single_subnet" {
  command = plan

  variables {
    subnet_ids = ["subnet-0aaaaaaaaaaaaaaa1"]
  }

  expect_failures = [var.subnet_ids]
}

run "rejects_cidr_instead_of_security_group" {
  command = plan

  variables {
    allowed_security_group_ids = ["0.0.0.0/0"]
  }

  expect_failures = [var.allowed_security_group_ids]
}

run "rejects_unsupported_engine_version" {
  command = plan

  variables {
    engine_version = "12"
  }

  expect_failures = [var.engine_version]
}

run "rejects_reserved_master_username" {
  command = plan

  variables {
    master_username = "postgres"
  }

  expect_failures = [var.master_username]
}

run "rejects_trailing_hyphen_name" {
  command = plan

  variables {
    name = "orders-"
  }

  expect_failures = [var.name]
}

run "rejects_kms_alias" {
  command = plan

  variables {
    kms_key_arn = "alias/aws/rds"
  }

  expect_failures = [var.kms_key_arn]
}

run "rejects_storage_ceiling_below_size" {
  command = plan

  variables {
    allocated_storage_gb     = 100
    max_allocated_storage_gb = 50
  }

  expect_failures = [var.max_allocated_storage_gb]
}
