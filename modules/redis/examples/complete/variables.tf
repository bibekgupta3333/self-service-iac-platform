variable "localstack_endpoint" {
  description = "LocalStack edge endpoint. Only STS is called (by aws_caller_identity) while planning."
  type        = string
  default     = "http://localhost:4566"
}

variable "name" {
  description = "Logical name of the example cache."
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
  default     = "redis-example"
}

variable "availability_zones" {
  description = "Availability Zones for the stand-in private subnets (literal, so planning needs no EC2 call)."
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}
