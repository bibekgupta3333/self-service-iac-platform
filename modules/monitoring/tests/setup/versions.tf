terraform {
  required_version = ">= 1.10, < 2.0"

  # The fixture has no aws resources of its own, but terraform test only hands
  # the test file's LocalStack provider to a run module that requires aws;
  # without this, the nested sqs module would use an unconfigured provider.
  required_providers {
    # tflint-ignore: terraform_unused_required_providers
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60, < 6.0"
    }
  }
}
