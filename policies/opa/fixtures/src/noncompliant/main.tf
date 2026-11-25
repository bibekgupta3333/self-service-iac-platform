# Non-compliant stack: breaks every OPA rule at least once. Plan is generated
# offline with dummy credentials (see ../../regenerate.sh); nothing is applied.

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
  region                      = "eu-central-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  default_tags {
    tags = {
      "iacp:team"        = "payments"
      "iacp:environment" = "staging"
    }
  }
}

module "store" {
  # Rewritten in plan.json to a ?ref=main git source by regenerate.sh.
  source = "../store"

  name          = "orders"
  secure        = false
  force_destroy = true
}

resource "aws_s3_bucket_policy" "public" {
  bucket = module.store.bucket
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::orders-data/*"
    }]
  })
}
