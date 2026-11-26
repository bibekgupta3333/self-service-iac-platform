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
    condition     = toset([for s in jsondecode(aws_s3_bucket_policy.this.policy).Statement : s.Sid]) == toset(["DenyInsecureTransport", "DenyOutdatedTls"])
    error_message = "Bucket policy must hold exactly the TLS-only and minimum-TLS-version denies."
  }

  assert {
    condition     = strcontains(aws_s3_bucket_policy.this.policy, "aws:SecureTransport") && strcontains(aws_s3_bucket_policy.this.policy, "s3:TlsVersion")
    error_message = "Bucket policy must deny non-TLS requests and TLS older than 1.2."
  }

  assert {
    condition     = length(jsondecode(aws_kms_key.this[0].policy).Statement) == 1
    error_message = "Without key_service_principals the key policy must only trust the account."
  }

  assert {
    condition     = aws_s3_bucket.this.tags_all["iacp:team"] == "platform" && aws_s3_bucket.this.tags_all["iacp:environment"] == "development" && aws_s3_bucket.this.tags_all["iacp:stack"] == "tftest" && aws_s3_bucket.this.tags_all["iacp:managed-by"] == "iacp" && aws_s3_bucket.this.tags_all["iacp:module"] == "s3"
    error_message = "Bucket must carry the mandatory iacp:* tags with the stack's values."
  }

  assert {
    condition     = aws_kms_key.this[0].tags_all["iacp:stack"] == "tftest" && aws_kms_key.this[0].tags_all["iacp:team"] == "platform"
    error_message = "The KMS key must carry the stack's iacp:* tags."
  }

  assert {
    condition     = aws_s3_bucket.this.tags_all["cost-center"] == "cc-1234"
    error_message = "User tags must be applied alongside platform tags."
  }

  assert {
    condition     = length(aws_s3_bucket_logging.this) == 0
    error_message = "Access logging must be off when access_logging is null."
  }

  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.this.rule) == 1 && length(one(aws_s3_bucket_lifecycle_configuration.this.rule).transition) == 0
    error_message = "A single lifecycle rule without a transition is expected by default."
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

run "lifecycle_transition_and_extras" {
  command = apply

  variables {
    transition_to_ia_days  = 60
    key_service_principals = ["logging.s3.amazonaws.com"]
    additional_policy_statements = [{
      sid                   = "AllowLogDelivery"
      effect                = "Allow"
      actions               = ["s3:PutObject"]
      principal_type        = "Service"
      principal_identifiers = ["logging.s3.amazonaws.com"]
    }]
  }

  assert {
    condition     = one(one(aws_s3_bucket_lifecycle_configuration.this.rule).transition).storage_class == "STANDARD_IA"
    error_message = "Setting transition_to_ia_days must add the STANDARD_IA transition."
  }

  assert {
    condition     = contains([for s in jsondecode(aws_s3_bucket_policy.this.policy).Statement : s.Sid], "AllowLogDelivery")
    error_message = "additional_policy_statements must be appended to the bucket policy."
  }

  assert {
    condition     = contains([for s in jsondecode(aws_kms_key.this[0].policy).Statement : s.Sid], "ServicePrincipalsForThisAccount")
    error_message = "key_service_principals must add a scoped key policy statement."
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

# Separate state: the provider refuses to plan Enabled -> Disabled on the bucket
# applied above, and this run is about a brand-new bucket.
run "unversioned_bucket_uses_disabled" {
  command   = plan
  state_key = "unversioned"

  variables {
    name               = "tftest-s3-unversioned"
    versioning_enabled = false
  }

  assert {
    condition     = one(aws_s3_bucket_versioning.this.versioning_configuration).status == "Disabled"
    error_message = "A new unversioned bucket must use Disabled, not Suspended."
  }
}

run "rejects_invalid_name" {
  command = plan

  variables {
    name = "Bad_Name"
  }

  expect_failures = [var.name]
}

# A valid 41-character name with a long team passes the name pattern but makes
# <team>-<environment>-<name> 64 characters, so only the length rule fails.
run "rejects_too_long_bucket_name" {
  command = plan

  variables {
    team = "platform-a"
    name = "a-valid-but-long-logical-bucket-name-1234"
  }

  expect_failures = [var.name]
}

run "accepts_63_character_bucket_name" {
  command = plan

  variables {
    team = "platforms"
    name = "a-valid-but-long-logical-bucket-name-1234"
  }

  assert {
    condition     = length(aws_s3_bucket.this.bucket) == 63
    error_message = "A 63-character bucket name must be accepted."
  }
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

run "rejects_public_allow_statement" {
  command = plan

  variables {
    additional_policy_statements = [{
      sid                   = "PublicRead"
      effect                = "Allow"
      actions               = ["s3:GetObject"]
      principal_type        = "*"
      principal_identifiers = ["*"]
    }]
  }

  expect_failures = [var.additional_policy_statements]
}

run "rejects_invalid_service_principal" {
  command = plan

  variables {
    key_service_principals = ["arn:aws:iam::123456789012:root"]
  }

  expect_failures = [var.key_service_principals]
}
