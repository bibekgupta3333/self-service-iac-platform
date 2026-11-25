variable "region" {
  description = "AWS region used by the LocalStack provider."
  type        = string
  default     = "us-east-1"
}

variable "localstack_endpoint" {
  description = "LocalStack edge endpoint."
  type        = string
  default     = "http://localhost:4566"
}

variable "name" {
  description = "Logical name of the example bucket."
  type        = string
  default     = "example-complete"
}

variable "team" {
  description = "Owning team slug."
  type        = string
  default     = "platform"
}

variable "environment" {
  description = "Target environment."
  type        = string
  default     = "development"
}

variable "stack" {
  description = "Owning stack name."
  type        = string
  default     = "s3-example"
}
