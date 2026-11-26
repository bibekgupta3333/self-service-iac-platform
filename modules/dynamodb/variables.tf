# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical table name. The final name is <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name))
    error_message = "name must be 3-41 characters of lowercase letters, digits and hyphens, starting with a letter."
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

# ---- Keys ----

variable "hash_key" {
  description = "Partition key attribute name."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9_.-]{1,255}$", var.hash_key))
    error_message = "hash_key must be 1-255 characters of letters, digits, '_', '.' and '-'."
  }
}

variable "hash_key_type" {
  description = "Partition key type: S (string), N (number) or B (binary)."
  type        = string
  default     = "S"

  validation {
    condition     = contains(["S", "N", "B"], var.hash_key_type)
    error_message = "hash_key_type must be S, N or B."
  }
}

variable "range_key" {
  description = "Optional sort key attribute name. Null creates a table with a partition key only."
  type        = string
  default     = null

  validation {
    condition     = var.range_key == null || try(can(regex("^[A-Za-z0-9_.-]{1,255}$", var.range_key)) && var.range_key != var.hash_key, false)
    error_message = "range_key must be 1-255 characters of letters, digits, '_', '.' and '-', and differ from hash_key."
  }
}

variable "range_key_type" {
  description = "Sort key type: S (string), N (number) or B (binary). Ignored without range_key."
  type        = string
  default     = "S"

  validation {
    condition     = contains(["S", "N", "B"], var.range_key_type)
    error_message = "range_key_type must be S, N or B."
  }
}

# ---- Capacity ----

variable "billing_mode" {
  description = "PAY_PER_REQUEST (on-demand, the default) or PROVISIONED (requires read_capacity and write_capacity)."
  type        = string
  default     = "PAY_PER_REQUEST"

  validation {
    condition     = contains(["PAY_PER_REQUEST", "PROVISIONED"], var.billing_mode)
    error_message = "billing_mode must be PAY_PER_REQUEST or PROVISIONED."
  }
}

variable "read_capacity" {
  description = "Provisioned read capacity units (1-40000). Required with PROVISIONED billing, and must be null with PAY_PER_REQUEST."
  type        = number
  default     = null

  validation {
    condition     = var.read_capacity == null || try(var.read_capacity >= 1 && var.read_capacity <= 40000 && floor(var.read_capacity) == var.read_capacity, false)
    error_message = "read_capacity must be a whole number between 1 and 40000."
  }

  validation {
    condition     = (var.billing_mode == "PROVISIONED") == (var.read_capacity != null)
    error_message = "read_capacity is required with PROVISIONED billing and not allowed with PAY_PER_REQUEST."
  }
}

variable "write_capacity" {
  description = "Provisioned write capacity units (1-40000). Required with PROVISIONED billing, and must be null with PAY_PER_REQUEST."
  type        = number
  default     = null

  validation {
    condition     = var.write_capacity == null || try(var.write_capacity >= 1 && var.write_capacity <= 40000 && floor(var.write_capacity) == var.write_capacity, false)
    error_message = "write_capacity must be a whole number between 1 and 40000."
  }

  validation {
    condition     = (var.billing_mode == "PROVISIONED") == (var.write_capacity != null)
    error_message = "write_capacity is required with PROVISIONED billing and not allowed with PAY_PER_REQUEST."
  }
}

variable "table_class" {
  description = "Storage class: STANDARD or STANDARD_INFREQUENT_ACCESS."
  type        = string
  default     = "STANDARD"

  validation {
    condition     = contains(["STANDARD", "STANDARD_INFREQUENT_ACCESS"], var.table_class)
    error_message = "table_class must be STANDARD or STANDARD_INFREQUENT_ACCESS."
  }
}

# ---- Indexes ----

variable "global_secondary_indexes" {
  description = <<-EOT
    Global secondary indexes (at most 20). Each has a unique name, a hash_key (and optional range_key) with
    types S, N or B, and a projection_type of ALL (default), KEYS_ONLY or INCLUDE. non_key_attributes is
    required with INCLUDE and not allowed otherwise. read_capacity and write_capacity apply only with
    PROVISIONED billing and default to the table's capacity. A key attribute shared with the table or another
    index must use the same type everywhere.
  EOT
  type = list(object({
    name               = string
    hash_key           = string
    hash_key_type      = optional(string, "S")
    range_key          = optional(string)
    range_key_type     = optional(string, "S")
    projection_type    = optional(string, "ALL")
    non_key_attributes = optional(list(string), [])
    read_capacity      = optional(number)
    write_capacity     = optional(number)
  }))
  default  = []
  nullable = false

  validation {
    condition     = length(var.global_secondary_indexes) <= 20 && length(distinct([for g in var.global_secondary_indexes : g.name])) == length(var.global_secondary_indexes)
    error_message = "At most 20 global secondary indexes are allowed and their names must be unique."
  }

  validation {
    condition = alltrue([
      for g in var.global_secondary_indexes :
      can(regex("^[A-Za-z0-9_.-]{3,255}$", g.name)) &&
      can(regex("^[A-Za-z0-9_.-]{1,255}$", g.hash_key)) &&
      (g.range_key == null || try(can(regex("^[A-Za-z0-9_.-]{1,255}$", g.range_key)) && g.range_key != g.hash_key, false)) &&
      contains(["S", "N", "B"], g.hash_key_type) && contains(["S", "N", "B"], g.range_key_type)
    ])
    error_message = "Each index needs a 3-255 character name, a valid hash_key, an optional range_key different from hash_key, and key types S, N or B."
  }

  validation {
    condition = alltrue([
      for g in var.global_secondary_indexes :
      contains(["ALL", "KEYS_ONLY", "INCLUDE"], g.projection_type) &&
      (g.projection_type == "INCLUDE") == (length(g.non_key_attributes) > 0) &&
      length(g.non_key_attributes) <= 100
    ])
    error_message = "projection_type must be ALL, KEYS_ONLY or INCLUDE; non_key_attributes (at most 100) is required with INCLUDE and not allowed otherwise."
  }

  validation {
    condition = alltrue([
      for g in var.global_secondary_indexes : var.billing_mode == "PROVISIONED" ? alltrue([
        for c in [g.read_capacity, g.write_capacity] : c == null || try(c >= 1 && c <= 40000 && floor(c) == c, false)
      ]) : (g.read_capacity == null && g.write_capacity == null)
    ])
    error_message = "Index read_capacity and write_capacity must be whole numbers between 1 and 40000, and are only allowed with PROVISIONED billing."
  }

  validation {
    # Group every key attribute by name; each name must map to a single type.
    condition = alltrue([
      for types in {
        for a in concat(
          [{ name = var.hash_key, type = var.hash_key_type }],
          var.range_key == null ? [] : [{ name = var.range_key, type = var.range_key_type }],
          flatten([for g in var.global_secondary_indexes : concat(
            [{ name = g.hash_key, type = g.hash_key_type }],
            g.range_key == null ? [] : [{ name = g.range_key, type = g.range_key_type }],
          )]),
        ) : a.name => a.type...
      } : length(distinct(types)) == 1
    ])
    error_message = "A key attribute used by the table and its indexes must have the same type everywhere."
  }
}

# ---- Data lifecycle and recovery ----

variable "ttl_attribute_name" {
  description = "Optional attribute holding an expiry time (epoch seconds). Null leaves TTL disabled."
  type        = string
  default     = null

  validation {
    condition     = var.ttl_attribute_name == null || can(regex("^[A-Za-z0-9_.-]{1,255}$", var.ttl_attribute_name))
    error_message = "ttl_attribute_name must be 1-255 characters of letters, digits, '_', '.' and '-'."
  }
}

variable "stream_view_type" {
  description = "Enables DynamoDB Streams with this view type: KEYS_ONLY, NEW_IMAGE, OLD_IMAGE or NEW_AND_OLD_IMAGES. Null leaves streams off."
  type        = string
  default     = null

  validation {
    condition     = var.stream_view_type == null || contains(["KEYS_ONLY", "NEW_IMAGE", "OLD_IMAGE", "NEW_AND_OLD_IMAGES"], coalesce(var.stream_view_type, "-"))
    error_message = "stream_view_type must be KEYS_ONLY, NEW_IMAGE, OLD_IMAGE or NEW_AND_OLD_IMAGES."
  }
}

variable "deletion_protection_enabled" {
  description = "Deletion protection. Null (the default) turns it on outside development. It cannot be turned off in staging or production."
  type        = bool
  default     = null

  validation {
    condition     = var.deletion_protection_enabled != false || var.environment == "development"
    error_message = "deletion_protection_enabled cannot be false outside development."
  }
}

# ---- Encryption ----

variable "kms_key_arn" {
  description = "Existing KMS key ARN for server-side encryption. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}
