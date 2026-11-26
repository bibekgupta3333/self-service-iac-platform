terraform {
  required_version = ">= 1.10, < 2.0"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # 5.70 is the first release that can tag aws_lambda_event_source_mapping.
      version = ">= 5.70, < 6.0"
    }
  }
}
