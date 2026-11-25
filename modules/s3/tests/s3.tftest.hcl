# terraform test suite for modules/s3. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/s3 test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    iam = "http://localhost:4566"
    kms = "http://localhost:4566"
    s3  = "http://localhost:4566"
    sts = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-s3"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  tags        = { "cost-center" = "cc-1234" }
}

run "secure_defaults" {
  command = apply

  assert {
    condition     = aws_s3_bucket.this.bucket == "platform-development-tftest-s3"
    error_message = "Bucket name must be <team>-<environment>-<name>."
  }

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.this.block_public_acls,
      aws_s3_bucket_public_access_block.this.block_public_policy,
      aws_s3_bucket_public_access_block.this.ignore_public_acls,
      aws_s3_bucket_public_access_block.this.restrict_public_buckets,
    ])
    error_message = "All four public access block flags must be enabled."
  }

  assert {
    condition     = one(aws_s3_bucket_ownership_controls.this.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "Object ownership must be BucketOwnerEnforced (ACLs disabled)."
  }

  assert {
    condition     = one(aws_s3_bucket_versioning.this.versioning_configuration).status == "Enabled"
    error_message = "Versioning must be enabled by default."
  }

  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.this.rule).apply_server_side_encryption_by_default).sse_algorithm == "aws:kms"
    error_message = "Bucket must use SSE-KMS."
  }

  assert {
    condition     = length(aws_kms_key.this) == 1 && aws_kms_key.this[0].enable_key_rotation
    error_message = "A dedicated KMS key with rotation must be created when kms_key_arn is null."
  }

  assert {
    condition     = output.kms_key_arn == aws_kms_key.this[0].arn
    error_message = "kms_key_arn output must point at the module-created key."
  }

  assert {
    condition     = strcontains(aws_s3_bucket_policy.this.policy, "aws:SecureTransport")
    error_message = "Bucket policy must deny non-TLS requests."
  }

  assert {
    condition = alltrue([
      for k in ["iacp:team", "iacp:environment", "iacp:stack", "iacp:managed-by"] :
      contains(keys(aws_s3_bucket.this.tags_all), k)
    ])
    error_message = "Bucket must carry every mandatory iacp:* tag."
  }

  assert {
    condition     = aws_s3_bucket.this.tags_all["iacp:managed-by"] == "iacp" && aws_s3_bucket.this.tags_all["cost-center"] == "cc-1234"
    error_message = "Platform and user tags must both be applied."
  }

  assert {
    condition     = length(aws_s3_bucket_logging.this) == 0
    error_message = "Access logging must be off when access_logging is null."
  }

  assert {
    condition     = output.iacp_metadata == { module = "s3", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "consumer_policies_are_scoped" {
  command = plan

  assert {
    condition     = !strcontains(output.read_policy_json, "s3:PutObject")
    error_message = "read_policy_json must not grant write actions."
  }

  assert {
    condition     = strcontains(output.write_policy_json, "s3:PutObject") && strcontains(output.write_policy_json, "s3:GetObject")
    error_message = "write_policy_json must grant read and write actions."
  }
}

run "lifecycle_transition_enabled" {
  command = apply

  variables {
    transition_to_ia_days = 60
  }

  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.this.rule) == 3
    error_message = "Setting transition_to_ia_days must add the STANDARD_IA transition rule."
  }
}

run "production_forces_versioning" {
  command = plan

  variables {
    environment        = "production"
    versioning_enabled = false
  }

  assert {
    condition     = one(aws_s3_bucket_versioning.this.versioning_configuration).status == "Enabled"
    error_message = "Versioning must stay enabled in production."
  }

  assert {
    condition     = aws_kms_key.this[0].deletion_window_in_days == 30
    error_message = "Production keys must use a 30-day deletion window."
  }
}

run "rejects_invalid_name" {
  command = plan

  variables {
    name = "Bad_Name"
  }

  expect_failures = [var.name]
}

run "rejects_too_long_bucket_name" {
  command = plan

  variables {
    name = "a-very-long-logical-bucket-name-over-limit"
  }

  expect_failures = [var.name]
}

run "rejects_reserved_tag_prefix" {
  command = plan

  variables {
    tags = { "iacp:team" = "someone-else" }
  }

  expect_failures = [var.tags]
}

run "rejects_force_destroy_in_production" {
  command = plan

  variables {
    environment   = "production"
    force_destroy = true
  }

  expect_failures = [var.force_destroy]
}

run "rejects_kms_alias" {
  command = plan

  variables {
    kms_key_arn = "alias/my-key"
  }

  expect_failures = [var.kms_key_arn]
}

run "rejects_short_ia_transition" {
  command = plan

  variables {
    transition_to_ia_days = 10
  }

  expect_failures = [var.transition_to_ia_days]
}
