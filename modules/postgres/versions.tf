terraform {
  required_version = ">= 1.10, < 2.0"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # Same range as the other platform modules; it supports
      # manage_master_user_password with a customer-managed key.
      version = ">= 5.70, < 6.0"
    }
    # Suffix of the final snapshot name (snapshot.tf). Runs locally, no API.
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6, < 4.0"
    }
  }
}
