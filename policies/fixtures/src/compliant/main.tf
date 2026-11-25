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
