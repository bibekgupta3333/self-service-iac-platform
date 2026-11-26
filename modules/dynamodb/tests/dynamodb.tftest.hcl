# terraform test suite for modules/dynamodb. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/dynamodb test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    dynamodb = "http://localhost:4566"
    iam      = "http://localhost:4566"
    kms      = "http://localhost:4566"
    sts      = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-ddb"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  hash_key    = "pk"
}

run "secure_defaults" {
  command = apply

  assert {
    condition     = aws_dynamodb_table.this.name == "platform-development-tftest-ddb"
    error_message = "Table name must be <team>-<environment>-<name>."
  }

  assert {
    condition     = aws_dynamodb_table.this.billing_mode == "PAY_PER_REQUEST"
    error_message = "Billing must default to on-demand."
  }

  assert {
    condition     = one(aws_dynamodb_table.this.point_in_time_recovery).enabled
    error_message = "Point-in-time recovery must be on."
  }

  assert {
    condition     = one(aws_dynamodb_table.this.server_side_encryption).enabled && one(aws_dynamodb_table.this.server_side_encryption).kms_key_arn == aws_kms_key.this[0].arn
    error_message = "The table must be encrypted with the module-created KMS key."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation
    error_message = "The module-created key must have rotation enabled."
  }

  assert {
    condition     = !aws_dynamodb_table.this.deletion_protection_enabled
    error_message = "Deletion protection must default to off in development."
  }

  assert {
    condition     = !aws_dynamodb_table.this.stream_enabled && output.stream_arn == null
    error_message = "Streams must be off by default and stream_arn must be null."
  }

  assert {
    condition     = [for a in aws_dynamodb_table.this.attribute : "${a.name}=${a.type}"] == ["pk=S"]
    error_message = "Only the partition key may be defined as an attribute."
  }

  assert {
    condition     = { for k, v in aws_dynamodb_table.this.tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "dynamodb" }
    error_message = "The table must carry exactly the platform tags with the stack's values."
  }

  assert {
    condition     = { for k, v in aws_kms_key.this[0].tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "dynamodb" }
    error_message = "The KMS key must carry the platform tags with the stack's values."
  }

  assert {
    condition     = length(jsondecode(aws_kms_key.this[0].policy).Statement) == 1
    error_message = "The key policy must only trust the owning account."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(output.read_policy_json).Statement :
      !anytrue([for a in flatten([s.Action]) : strcontains(a, "*") || contains(["dynamodb:PutItem", "dynamodb:DeleteItem", "dynamodb:UpdateItem"], a)])
    ])
    error_message = "read_policy_json must use explicit read actions only."
  }

  assert {
    condition     = !strcontains(output.read_policy_json, "dynamodb:GetRecords") && !strcontains(output.read_policy_json, "dynamodb:ListStreams")
    error_message = "Without a stream, read_policy_json must not grant stream access."
  }

  assert {
    condition     = strcontains(output.write_policy_json, "dynamodb:PutItem") && !strcontains(output.write_policy_json, "dynamodb:Scan")
    error_message = "write_policy_json must grant writes but not scans."
  }

  assert {
    condition     = output.iacp_metadata == { module = "dynamodb", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "composite_key_gsi_ttl_stream" {
  command = apply

  variables {
    name           = "tftest-ddb-full"
    range_key      = "sk"
    range_key_type = "N"
    global_secondary_indexes = [
      { name = "by-status", hash_key = "status", range_key = "sk", range_key_type = "N", projection_type = "INCLUDE", non_key_attributes = ["total"] },
      { name = "by-owner", hash_key = "owner", projection_type = "KEYS_ONLY" },
    ]
    ttl_attribute_name = "expires_at"
    stream_view_type   = "NEW_AND_OLD_IMAGES"
  }

  assert {
    condition     = toset([for a in aws_dynamodb_table.this.attribute : "${a.name}=${a.type}"]) == toset(["pk=S", "sk=N", "status=S", "owner=S"])
    error_message = "Attributes must be the distinct key attributes of the table and its indexes."
  }

  assert {
    condition     = length(aws_dynamodb_table.this.global_secondary_index) == 2
    error_message = "Both global secondary indexes must be created."
  }

  assert {
    condition = anytrue([
      for g in aws_dynamodb_table.this.global_secondary_index :
      g.name == "by-status" && g.projection_type == "INCLUDE" && toset(g.non_key_attributes) == toset(["total"])
    ])
    error_message = "INCLUDE projections must carry their non-key attributes."
  }

  assert {
    condition     = one(aws_dynamodb_table.this.ttl).enabled && one(aws_dynamodb_table.this.ttl).attribute_name == "expires_at"
    error_message = "TTL must be enabled on ttl_attribute_name."
  }

  assert {
    condition     = aws_dynamodb_table.this.stream_enabled && aws_dynamodb_table.this.stream_view_type == "NEW_AND_OLD_IMAGES" && output.stream_arn == aws_dynamodb_table.this.stream_arn && output.stream_arn != null
    error_message = "stream_view_type must enable the stream and expose its ARN."
  }

  assert {
    condition = anytrue([
      for st in jsondecode(output.read_policy_json).Statement :
      st.Sid == "ReadStream" && flatten([st.Resource]) == [aws_dynamodb_table.this.stream_arn] &&
      toset(flatten([st.Action])) == toset(["dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator"])
    ])
    error_message = "With a stream, read_policy_json must grant stream reads on that stream only."
  }
}

run "deletion_protection_outside_development" {
  command = plan

  variables {
    environment = "staging"
  }

  assert {
    condition     = aws_dynamodb_table.this.deletion_protection_enabled
    error_message = "Deletion protection must default to on outside development."
  }

  assert {
    condition     = aws_kms_key.this[0].deletion_window_in_days == 7
    error_message = "Non-production keys use a 7-day deletion window."
  }
}

run "production_key_window" {
  command = plan

  variables {
    environment = "production"
  }

  assert {
    condition     = aws_dynamodb_table.this.deletion_protection_enabled && aws_kms_key.this[0].deletion_window_in_days == 30
    error_message = "Production tables need deletion protection and a 30-day key deletion window."
  }
}

run "provisioned_capacity" {
  command = plan

  variables {
    billing_mode   = "PROVISIONED"
    read_capacity  = 5
    write_capacity = 2
    global_secondary_indexes = [
      { name = "by-owner", hash_key = "owner", read_capacity = 3 },
    ]
  }

  assert {
    condition     = aws_dynamodb_table.this.read_capacity == 5 && aws_dynamodb_table.this.write_capacity == 2
    error_message = "Provisioned capacity must flow into the table."
  }

  assert {
    condition     = one(aws_dynamodb_table.this.global_secondary_index).read_capacity == 3 && one(aws_dynamodb_table.this.global_secondary_index).write_capacity == 2
    error_message = "Index capacity must default to the table's capacity."
  }
}

run "existing_key" {
  command = plan

  variables {
    kms_key_arn = "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && one(aws_dynamodb_table.this.server_side_encryption).kms_key_arn == "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
    error_message = "An existing kms_key_arn must be used instead of creating a key."
  }
}

run "rejects_disabled_deletion_protection_in_production" {
  command = plan

  variables {
    environment                 = "production"
    deletion_protection_enabled = false
  }

  expect_failures = [var.deletion_protection_enabled]
}

run "rejects_capacity_with_on_demand" {
  command = plan

  variables {
    read_capacity = 5
  }

  expect_failures = [var.read_capacity]
}

run "rejects_provisioned_without_capacity" {
  command = plan

  variables {
    billing_mode = "PROVISIONED"
  }

  expect_failures = [var.read_capacity, var.write_capacity]
}

run "rejects_conflicting_attribute_types" {
  command = plan

  variables {
    global_secondary_indexes = [
      { name = "by-pk", hash_key = "pk", hash_key_type = "N" },
    ]
  }

  expect_failures = [var.global_secondary_indexes]
}

run "rejects_include_without_attributes" {
  command = plan

  variables {
    global_secondary_indexes = [
      { name = "by-owner", hash_key = "owner", projection_type = "INCLUDE" },
    ]
  }

  expect_failures = [var.global_secondary_indexes]
}

run "rejects_duplicate_index_names" {
  command = plan

  variables {
    global_secondary_indexes = [
      { name = "by-owner", hash_key = "owner" },
      { name = "by-owner", hash_key = "status" },
    ]
  }

  expect_failures = [var.global_secondary_indexes]
}

run "rejects_range_key_equal_to_hash_key" {
  command = plan

  variables {
    range_key = "pk"
  }

  expect_failures = [var.range_key]
}

run "rejects_unknown_stream_view_type" {
  command = plan

  variables {
    stream_view_type = "ALL"
  }

  expect_failures = [var.stream_view_type]
}

run "rejects_invalid_name" {
  command = plan

  variables {
    name = "UPPER"
  }

  expect_failures = [var.name]
}

run "rejects_reserved_tag_prefix" {
  command = plan

  variables {
    tags = { "iacp:managed-by" = "someone" }
  }

  expect_failures = [var.tags]
}
