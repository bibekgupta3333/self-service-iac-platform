# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical function name. The final name is <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name))
    error_message = "name must be 3-41 characters of lowercase letters, digits and hyphens, starting with a letter."
  }

  validation {
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 64
    error_message = "The computed function name <team>-<environment>-<name> must be at most 64 characters."
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

variable "runtime" {
  description = "Lambda runtime identifier."
  type        = string

  validation {
    condition     = contains(["python3.12", "python3.13", "nodejs20.x", "nodejs22.x", "java21", "provided.al2023"], var.runtime)
    error_message = "runtime must be one of python3.12, python3.13, nodejs20.x, nodejs22.x, java21, provided.al2023."
  }
}

variable "handler" {
  description = "Function entry point, for example app.handler. Use bootstrap for provided.al2023."
  type        = string
  default     = "bootstrap"

  validation {
    condition     = can(regex("^[A-Za-z0-9_.:/$-]{1,128}$", var.handler))
    error_message = "handler must be 1-128 characters of letters, digits and . _ : / $ -."
  }
}

variable "architecture" {
  description = "Instruction set architecture of the function."
  type        = string
  default     = "arm64"

  validation {
    condition     = contains(["arm64", "x86_64"], var.architecture)
    error_message = "architecture must be arm64 or x86_64."
  }
}

variable "artifact_s3_uri" {
  description = "s3://bucket/key of the deployment package (.zip), built by the application's CI."
  type        = string

  validation {
    condition     = can(regex("^s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]/.+\\.zip$", var.artifact_s3_uri))
    error_message = "artifact_s3_uri must be an s3:// URI to a .zip file."
  }
}

variable "source_code_hash" {
  description = "Base64-encoded SHA-256 of the deployment package. Changing it redeploys the code. Null skips hash tracking."
  type        = string
  default     = null
}

variable "memory_mb" {
  description = "Memory allocated to the function, in MB."
  type        = number
  default     = 256

  validation {
    condition     = var.memory_mb >= 128 && var.memory_mb <= 10240
    error_message = "memory_mb must be between 128 and 10240."
  }
}

variable "timeout_seconds" {
  description = "Maximum execution time, in seconds."
  type        = number
  default     = 30

  validation {
    condition     = var.timeout_seconds >= 1 && var.timeout_seconds <= 900
    error_message = "timeout_seconds must be between 1 and 900."
  }
}

variable "reserved_concurrent_executions" {
  description = "Concurrency reserved for the function. -1 removes the reservation (not allowed in production)."
  type        = number
  default     = 10

  validation {
    condition     = var.reserved_concurrent_executions >= -1 && var.reserved_concurrent_executions <= 1000
    error_message = "reserved_concurrent_executions must be between -1 and 1000."
  }

  validation {
    condition     = !(var.reserved_concurrent_executions == -1 && var.environment == "production")
    error_message = "Production functions must reserve concurrency."
  }
}

variable "tracing_mode" {
  description = "X-Ray tracing mode."
  type        = string
  default     = "Active"

  validation {
    condition     = contains(["Active", "PassThrough"], var.tracing_mode)
    error_message = "tracing_mode must be Active or PassThrough."
  }
}

variable "environment_variables" {
  description = "Non-secret configuration. Pass Secrets Manager ARNs, never secret values."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for k in keys(var.environment_variables) : can(regex("^[A-Za-z][A-Za-z0-9_]*$", k)) && !startswith(k, "AWS_")])
    error_message = "environment_variables keys must be letters, digits and underscores, start with a letter, and not use the reserved AWS_ prefix."
  }
}

variable "log_retention_days" {
  description = "Days CloudWatch keeps function logs."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a retention period CloudWatch Logs supports (for example 7, 14, 30, 90, 365)."
  }
}

variable "kms_key_arn" {
  description = "Existing KMS key ARN for environment variables and logs. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}

variable "sqs_trigger" {
  description = "Optional SQS trigger. queue_arn is wireable from the sqs module's arn output; batch_size is the maximum messages per invocation. An object (not a plain ARN string) so the queue may be created in the same configuration."
  type = object({
    queue_arn  = string
    batch_size = optional(number, 10)
  })
  default = null

  validation {
    condition     = var.sqs_trigger == null || can(regex("^arn:aws[a-z-]*:sqs:[a-z0-9-]+:[0-9]{12}:.+$", var.sqs_trigger.queue_arn))
    error_message = "sqs_trigger.queue_arn must be an SQS queue ARN."
  }

  validation {
    condition     = var.sqs_trigger == null || try(var.sqs_trigger.batch_size >= 1 && var.sqs_trigger.batch_size <= 10000, false)
    error_message = "sqs_trigger.batch_size must be between 1 and 10000."
  }
}

variable "dead_letter_target_arn" {
  description = "Optional SQS queue or SNS topic ARN that receives failed asynchronous invocations."
  type        = string
  default     = null

  validation {
    condition     = var.dead_letter_target_arn == null || can(regex("^arn:aws[a-z-]*:(sqs|sns):[a-z0-9-]+:[0-9]{12}:.+$", var.dead_letter_target_arn))
    error_message = "dead_letter_target_arn must be an SQS queue or SNS topic ARN."
  }
}

variable "permissions_boundary_arn" {
  description = "IAM permissions boundary attached to the execution role (the platform's iacp-boundary in shared accounts). Null attaches none."
  type        = string
  default     = null

  validation {
    condition     = var.permissions_boundary_arn == null || can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:policy/.+$", var.permissions_boundary_arn))
    error_message = "permissions_boundary_arn must be an IAM policy ARN."
  }
}

variable "extra_policy_jsons" {
  description = "Additional IAM policy documents for the execution role, for example s3 read_policy_json or sqs consumer_policy_json. Wildcard actions are rejected."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.extra_policy_jsons) <= 10
    error_message = "At most 10 extra policy documents are allowed."
  }

  validation {
    condition = alltrue([
      for doc in var.extra_policy_jsons : try(alltrue([
        for s in jsondecode(doc).Statement : !contains(flatten([try(s.Action, [])]), "*")
      ]), false)
    ])
    error_message = "extra_policy_jsons must be valid IAM policy JSON and must not grant the '*' action."
  }
}
