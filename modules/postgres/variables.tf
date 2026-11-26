# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical database name. The DB instance identifier is <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name)) && !endswith(var.name, "-") && !strcontains(var.name, "--")
    error_message = "name must be 3-41 characters of lowercase letters, digits and single hyphens, starting with a letter and not ending with a hyphen."
  }

  validation {
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 63
    error_message = "The computed identifier <team>-<environment>-<name> must be at most 63 characters."
  }
}

variable "team" {
  description = "Owning team slug. Injected by the platform."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.team))
    error_message = "team must be 2-31 characters of lowercase letters, digits and hyphens, starting with a letter."
  }

  # team feeds the identifier <team>-<environment>-<name>, and RDS rejects
  # consecutive or trailing hyphens.
  validation {
    condition     = !endswith(var.team, "-") && !strcontains(var.team, "--")
    error_message = "team must not end with a hyphen or contain consecutive hyphens."
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

# ---- Network ----

variable "vpc_id" {
  description = "VPC that holds the database and its security group (for example the vpc module's id output)."
  type        = string

  validation {
    condition     = can(regex("^vpc-[0-9a-f]{8,17}$", var.vpc_id))
    error_message = "vpc_id must be a VPC ID such as vpc-0123456789abcdef0."
  }
}

variable "subnet_ids" {
  description = "Private subnets for the DB subnet group, in at least two Availability Zones (for example the vpc module's private_subnet_ids output)."
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 2 && length(var.subnet_ids) <= 16 && alltrue([for s in var.subnet_ids : can(regex("^subnet-[0-9a-f]{8,17}$", s))])
    error_message = "subnet_ids must list 2-16 subnet IDs such as subnet-0123456789abcdef0."
  }

  validation {
    condition     = length(distinct(var.subnet_ids)) == length(var.subnet_ids)
    error_message = "subnet_ids must not contain duplicates."
  }
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect on the database port (for example the ecs-service or lambda security group). Nothing else can reach the instance."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.allowed_security_group_ids) <= 20 && alltrue([for s in var.allowed_security_group_ids : can(regex("^sg-[0-9a-f]{8,17}$", s))])
    error_message = "allowed_security_group_ids must list at most 20 security group IDs such as sg-0123456789abcdef0."
  }
}

variable "port" {
  description = "TCP port the database listens on."
  type        = number
  default     = 5432

  validation {
    condition     = var.port >= 1150 && var.port <= 65535 && floor(var.port) == var.port
    error_message = "port must be an integer between 1150 and 65535."
  }
}

# ---- Engine and capacity ----

variable "engine_version" {
  description = "PostgreSQL major version (for example 16) or major.minor (16.4). Minor versions are upgraded automatically in the maintenance window."
  type        = string
  default     = "16"

  validation {
    condition     = can(regex("^(14|15|16|17)(\\.[0-9]+)?$", var.engine_version))
    error_message = "engine_version must be a supported PostgreSQL major version (14-17), optionally with a minor version."
  }
}

variable "instance_class" {
  description = "RDS instance class, for example db.t4g.micro or db.r7g.large."
  type        = string
  default     = "db.t4g.micro"

  validation {
    condition     = can(regex("^db\\.[a-z0-9]+\\.[a-z0-9]+$", var.instance_class))
    error_message = "instance_class must look like db.<family>.<size>, for example db.t4g.micro."
  }
}

variable "allocated_storage_gb" {
  description = "Initial gp3 storage in GiB."
  type        = number
  default     = 20

  validation {
    condition     = var.allocated_storage_gb >= 20 && var.allocated_storage_gb <= 65536 && floor(var.allocated_storage_gb) == var.allocated_storage_gb
    error_message = "allocated_storage_gb must be an integer between 20 and 65536."
  }
}

variable "max_allocated_storage_gb" {
  description = "Upper limit for storage autoscaling in GiB. Null disables autoscaling."
  type        = number
  default     = null

  validation {
    condition     = var.max_allocated_storage_gb == null || try(var.max_allocated_storage_gb >= var.allocated_storage_gb && var.max_allocated_storage_gb <= 65536, false)
    error_message = "max_allocated_storage_gb must be between allocated_storage_gb and 65536, or null."
  }
}

variable "multi_az" {
  description = "Run a standby in a second Availability Zone. Null means on in production and off elsewhere; false is rejected in production."
  type        = bool
  default     = null

  validation {
    condition     = !(var.multi_az == false && var.environment == "production")
    error_message = "Production databases must be Multi-AZ."
  }
}

variable "database_name" {
  description = "Name of the database created with the instance. Null creates none (connect to the default postgres database)."
  type        = string
  default     = null

  validation {
    condition     = var.database_name == null || can(regex("^[a-z][a-z0-9_]{0,62}$", var.database_name))
    error_message = "database_name must be 1-63 lowercase letters, digits and underscores, starting with a letter."
  }
}

variable "master_username" {
  description = "Master user name. Its password is generated and rotated by RDS in Secrets Manager and never passes through Terraform."
  type        = string
  default     = "iacp_admin"

  validation {
    condition     = can(regex("^[a-z][a-z0-9_]{0,62}$", var.master_username)) && !contains(["postgres", "rdsadmin", "admin", "root"], var.master_username)
    error_message = "master_username must be 1-63 lowercase letters, digits and underscores, start with a letter, and not be a reserved name (postgres, rdsadmin, admin, root)."
  }
}

# ---- Protection and recovery ----

variable "backup_retention_days" {
  description = "Days of automated backups (point-in-time recovery). Null means 7 outside production and 35 in production; production needs at least 7."
  type        = number
  default     = null

  validation {
    condition     = var.backup_retention_days == null || try(var.backup_retention_days >= 1 && var.backup_retention_days <= 35 && floor(var.backup_retention_days) == var.backup_retention_days, false)
    error_message = "backup_retention_days must be an integer between 1 and 35, or null."
  }

  validation {
    condition     = var.backup_retention_days == null || var.environment != "production" || try(var.backup_retention_days >= 7, false)
    error_message = "Production databases must keep at least 7 days of backups."
  }
}

variable "iam_database_authentication_enabled" {
  description = "Allow IAM database authentication in addition to passwords."
  type        = bool
  default     = true
}

variable "log_retention_days" {
  description = "Days CloudWatch keeps the exported postgresql and upgrade logs."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a retention period CloudWatch Logs supports (for example 7, 14, 30, 90, 365)."
  }
}

variable "kms_key_arn" {
  description = "Existing customer-managed KMS key ARN for storage, Performance Insights, logs and the master secret. If null, the module creates a dedicated key with rotation enabled. Must be known at plan time."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}
