# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical queue name. The final name is <team>-<environment>-<name> (plus .fifo for FIFO queues)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name))
    error_message = "name must be 3-41 characters of lowercase letters, digits and hyphens, starting with a letter."
  }

  validation {
    # Leave room for the "-dlq.fifo" suffix within the 80-character SQS limit.
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 71
    error_message = "The computed queue name <team>-<environment>-<name> must be at most 71 characters."
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

variable "fifo" {
  description = "Create FIFO queues (main and DLQ) instead of standard queues."
  type        = bool
  default     = false
}

variable "content_based_deduplication" {
  description = "Use a SHA-256 hash of the body for deduplication. FIFO queues only."
  type        = bool
  default     = false

  validation {
    condition     = !var.content_based_deduplication || var.fifo
    error_message = "content_based_deduplication requires fifo = true."
  }
}

variable "visibility_timeout_seconds" {
  description = "Seconds a received message stays hidden from other consumers."
  type        = number
  default     = 30

  validation {
    condition     = var.visibility_timeout_seconds >= 0 && var.visibility_timeout_seconds <= 43200
    error_message = "visibility_timeout_seconds must be between 0 and 43200."
  }
}

variable "message_retention_seconds" {
  description = "Seconds the main queue keeps a message."
  type        = number
  default     = 345600

  validation {
    condition     = var.message_retention_seconds >= 60 && var.message_retention_seconds <= 1209600
    error_message = "message_retention_seconds must be between 60 and 1209600."
  }
}

variable "receive_wait_time_seconds" {
  description = "Long-polling wait time for ReceiveMessage calls."
  type        = number
  default     = 20

  validation {
    condition     = var.receive_wait_time_seconds >= 0 && var.receive_wait_time_seconds <= 20
    error_message = "receive_wait_time_seconds must be between 0 and 20."
  }
}

variable "max_receive_count" {
  description = "Receives before a message moves to the dead-letter queue."
  type        = number
  default     = 5

  validation {
    condition     = var.max_receive_count >= 1 && var.max_receive_count <= 100
    error_message = "max_receive_count must be between 1 and 100."
  }
}

variable "dlq_retention_seconds" {
  description = "Seconds the dead-letter queue keeps a message. Must be at least the main queue retention."
  type        = number
  default     = 1209600

  validation {
    condition     = var.dlq_retention_seconds >= var.message_retention_seconds && var.dlq_retention_seconds <= 1209600
    error_message = "dlq_retention_seconds must be at least message_retention_seconds and at most 1209600."
  }
}

variable "kms_key_arn" {
  description = "Existing KMS key ARN for both queues. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}

variable "key_service_principals" {
  description = "AWS service principals (for example sns.amazonaws.com or events.amazonaws.com) allowed to use the module-created KMS key on behalf of this account, so they can deliver to the queue. Ignored when kms_key_arn is set."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for p in var.key_service_principals : can(regex("^[a-z0-9-]+(\\.[a-z0-9-]+)*\\.amazonaws\\.com$", p))])
    error_message = "key_service_principals must be AWS service principals such as sns.amazonaws.com."
  }
}

variable "kms_data_key_reuse_period_seconds" {
  description = "Seconds SQS reuses a data key before calling KMS again."
  type        = number
  default     = 300

  validation {
    condition     = var.kms_data_key_reuse_period_seconds >= 60 && var.kms_data_key_reuse_period_seconds <= 86400
    error_message = "kms_data_key_reuse_period_seconds must be between 60 and 86400."
  }
}

variable "alarm_actions" {
  description = "ARNs (for example SNS topics) notified when a queue alarm fires or recovers."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for a in var.alarm_actions : startswith(a, "arn:")])
    error_message = "alarm_actions must be ARNs."
  }
}

variable "oldest_message_age_alarm_seconds" {
  description = "Alarm when the oldest message in the main queue is older than this many seconds."
  type        = number
  default     = 3600

  validation {
    condition     = var.oldest_message_age_alarm_seconds >= 60
    error_message = "oldest_message_age_alarm_seconds must be at least 60."
  }
}
