# Compliant stack: every OPA rule passes. Plan is generated offline with dummy
# credentials (see ../../regenerate.sh); nothing is ever applied.

terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.0.0"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  default_tags {
    tags = {
      "iacp:team"        = "payments"
      "iacp:environment" = "staging"
      "iacp:stack"       = "orders"
      "iacp:managed-by"  = "iacp"
    }
  }
}

module "store" {
  # Rewritten in plan.json to the pinned git source by regenerate.sh.
  source = "../store"

  name   = "orders"
  secure = true
}

# A topic and a table encrypted with a customer-managed key created in the
# same plan (the key ARN is only known after apply).
resource "aws_kms_key" "data" {
  description         = "Encrypts the orders topic and table"
  enable_key_rotation = true
}

resource "aws_sns_topic" "events" {
  name              = "orders-events"
  kms_master_key_id = aws_kms_key.data.arn
}

resource "aws_dynamodb_table" "orders" {
  name         = "orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"

  attribute {
    name = "pk"
    type = "S"
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = aws_kms_key.data.arn
  }
}
