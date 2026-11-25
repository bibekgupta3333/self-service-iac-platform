# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical bucket name. The final name is <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name))
    error_message = "name must be 3-41 characters of lowercase letters, digits and hyphens, starting with a letter."
  }

  validation {
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 63
    error_message = "The computed bucket name <team>-<environment>-<name> must be at most 63 characters."
  }
}

variable "team" {
  description = "Owning team slug. Injected by the platform."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.team))
    error_message = "team must be 2-31 characters of lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "environment" {
  description = "Target environment. Injected by the platform."
  type        = string

  validation {
    condition     = contains(["development", "staging", "production"], var.environment)
    error_message = "environment must be one of development, staging, production."
  }
}

variable "stack" {
  description = "Name of the stack that owns this instance. Injected by the platform and used for the iacp:stack tag."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,62}$", var.stack))
    error_message = "stack must be 2-63 characters of lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "tags" {
  description = "Additional tags. Keys starting with 'iacp:' are reserved for platform tags."
  type        = map(string)
  default     = {}

  validation {
    condition     = length(var.tags) <= 30 && alltrue([for k in keys(var.tags) : !startswith(k, "iacp:")])
    error_message = "At most 30 tags are allowed and the 'iacp:' key prefix is reserved."
  }
}

# ---- Module inputs ----

variable "versioning_enabled" {
  description = "Keep object versions. Always enabled in production."
  type        = bool
  default     = true
}

variable "kms_key_arn" {
  description = "Existing KMS key ARN to encrypt the bucket. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}

variable "noncurrent_version_expiration_days" {
  description = "Days after which noncurrent object versions expire."
  type        = number
  default     = 90

  validation {
    condition     = var.noncurrent_version_expiration_days >= 1 && var.noncurrent_version_expiration_days <= 3650
    error_message = "noncurrent_version_expiration_days must be between 1 and 3650."
  }
}

variable "transition_to_ia_days" {
  description = "Days after which current objects move to STANDARD_IA. Null disables the transition."
  type        = number
  default     = null

  validation {
    condition     = var.transition_to_ia_days == null || try(var.transition_to_ia_days >= 30, false)
    error_message = "transition_to_ia_days must be at least 30 (the S3 minimum for STANDARD_IA)."
  }
}

variable "force_destroy" {
  description = "Allow the bucket to be destroyed while it still holds objects. Not allowed in production."
  type        = bool
  default     = false

  validation {
    condition     = !(var.force_destroy && var.environment == "production")
    error_message = "force_destroy cannot be enabled in production."
  }
}

variable "access_log_bucket" {
  description = "Name of an existing bucket that receives server access logs. Null disables access logging."
  type        = string
  default     = null

  validation {
    condition     = var.access_log_bucket == null || can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.access_log_bucket))
    error_message = "access_log_bucket must be a valid S3 bucket name."
  }
}
