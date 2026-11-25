variable "name" {
  description = "Base name for the bucket and queue."
  type        = string
}

variable "secure" {
  description = "Whether to apply the platform's secure defaults."
  type        = bool
}

variable "force_destroy" {
  description = "Allow destroying a non-empty bucket."
  type        = bool
  default     = false
}
