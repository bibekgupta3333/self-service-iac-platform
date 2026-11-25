# LocalStack provider configuration (ADR-0009). Only examples and tests
# configure endpoints; the module itself is provider-agnostic.
provider "aws" {
  region                      = var.region
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    iam = var.localstack_endpoint
    kms = var.localstack_endpoint
    s3  = var.localstack_endpoint
    sts = var.localstack_endpoint
  }
}
