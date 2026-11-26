# LocalStack provider configuration (ADR-0009). Only examples and tests
# configure endpoints; the module itself is provider-agnostic.
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    ec2  = var.localstack_endpoint
    iam  = var.localstack_endpoint
    kms  = var.localstack_endpoint
    logs = var.localstack_endpoint
    sts  = var.localstack_endpoint
  }
}
