variable "localstack_endpoint" {
  description = "LocalStack edge endpoint. Only STS is called (by aws_caller_identity) while planning."
  type        = string
  default     = "http://localhost:4566"
}

variable "name" {
  description = "Logical name of the example service (also prefixes the database)."
  type        = string
  default     = "example-api"
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
  default     = "ecs-service-example"
}

variable "availability_zones" {
  description = "Availability Zones for the stand-in private subnets (literal, so planning needs no EC2 call)."
  type        = list(string)
  default     = ["us-east-1a", "us-east-1b"]
}

variable "image_tag" {
  description = "Image tag CI would have pushed to the service's repository."
  type        = string
  default     = "1.0.0"
}

variable "certificate_arn" {
  description = "ACM certificate for the HTTPS listener (a placeholder in the LocalStack account; the plan does not look it up)."
  type        = string
  default     = "arn:aws:acm:us-east-1:000000000000:certificate/00000000-0000-0000-0000-000000000000"
}
