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
  description = "Keep object versions. Always enabled in production. Set it at creation: a versioned bucket can be suspended but never returned to unversioned."
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

variable "access_logging" {
  description = "Server access logging. target_bucket is an existing bucket that accepts log delivery; target_prefix defaults to s3/<bucket name>/. Null disables access logging. An object (not a plain string) so the target bucket may be another resource's output that is unknown until apply."
  type = object({
    target_bucket = string
    target_prefix = optional(string)
  })
  default = null

  validation {
    condition     = var.access_logging == null || can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.access_logging.target_bucket))
    error_message = "access_logging.target_bucket must be a valid S3 bucket name."
  }
}

variable "key_service_principals" {
  description = "AWS service principals (for example logging.s3.amazonaws.com) allowed to use the module-created KMS key on behalf of this account. Ignored when kms_key_arn is set."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for p in var.key_service_principals : can(regex("^[a-z0-9-]+(\\.[a-z0-9-]+)*\\.amazonaws\\.com$", p))])
    error_message = "key_service_principals must be AWS service principals such as logging.s3.amazonaws.com."
  }
}

variable "additional_policy_statements" {
  description = "Extra bucket policy statements, for example granting a service principal write access. resources defaults to the bucket and its objects. Allow statements must name specific principals."
  type = list(object({
    sid                   = string
    effect                = string
    actions               = list(string)
    principal_type        = string
    principal_identifiers = list(string)
    resources             = optional(list(string))
    conditions = optional(list(object({
      test     = string
      variable = string
      values   = list(string)
    })), [])
  }))
  default = []

  validation {
    condition     = length(var.additional_policy_statements) <= 10 && alltrue([for s in var.additional_policy_statements : can(regex("^[A-Za-z0-9]{1,64}$", s.sid)) && !contains(["DenyInsecureTransport", "DenyOutdatedTls"], s.sid)])
    error_message = "At most 10 statements; each sid must be 1-64 alphanumerics and must not reuse a built-in sid."
  }

  validation {
    condition     = alltrue([for s in var.additional_policy_statements : contains(["Allow", "Deny"], s.effect) && contains(["AWS", "Service", "CanonicalUser", "*"], s.principal_type)])
    error_message = "effect must be Allow or Deny, and principal_type must be AWS, Service, CanonicalUser or *."
  }

  validation {
    condition     = alltrue([for s in var.additional_policy_statements : s.effect == "Deny" || (s.principal_type != "*" && !contains(s.principal_identifiers, "*"))])
    error_message = "Allow statements must name specific principals; wildcard principals would make the bucket public."
  }
}
