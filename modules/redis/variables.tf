# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical cache name. The replication group ID is <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name)) && !endswith(var.name, "-") && !strcontains(var.name, "--")
    error_message = "name must be 3-41 characters of lowercase letters, digits and single hyphens, starting with a letter and not ending with a hyphen."
  }

  validation {
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 40
    error_message = "The computed replication group ID <team>-<environment>-<name> must be at most 40 characters."
  }
}

variable "team" {
  description = "Owning team slug. Injected by the platform."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.team))
    error_message = "team must be 2-31 characters of lowercase letters, digits and hyphens, starting with a letter."
  }

  # team feeds the replication group ID <team>-<environment>-<name>, and
  # ElastiCache rejects consecutive or trailing hyphens.
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
  description = "VPC that holds the cache and its security group (for example the vpc module's id output)."
  type        = string

  validation {
    condition     = can(regex("^vpc-[0-9a-f]{8,17}$", var.vpc_id))
    error_message = "vpc_id must be a VPC ID such as vpc-0123456789abcdef0."
  }
}

variable "subnet_ids" {
  description = "Private subnets for the cache subnet group, in at least two Availability Zones (for example the vpc module's private_subnet_ids output)."
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
  description = "Security groups allowed to connect on the cache port (for example the ecs-service or lambda security group). Nothing else can reach the cache."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.allowed_security_group_ids) <= 20 && alltrue([for s in var.allowed_security_group_ids : can(regex("^sg-[0-9a-f]{8,17}$", s))])
    error_message = "allowed_security_group_ids must list at most 20 security group IDs such as sg-0123456789abcdef0."
  }
}

variable "port" {
  description = "TCP port the cache listens on."
  type        = number
  default     = 6379

  validation {
    condition     = var.port >= 1024 && var.port <= 65535 && floor(var.port) == var.port
    error_message = "port must be an integer between 1024 and 65535."
  }
}

# ---- Engine and capacity ----

variable "engine_version" {
  description = "Redis OSS engine version (7.0 or 7.1)."
  type        = string
  default     = "7.1"

  validation {
    condition     = contains(["7.0", "7.1"], var.engine_version)
    error_message = "engine_version must be 7.0 or 7.1."
  }
}

variable "node_type" {
  description = "Cache node type, for example cache.t4g.micro or cache.r7g.large."
  type        = string
  default     = "cache.t4g.micro"

  validation {
    condition     = can(regex("^cache\\.[a-z0-9]+\\.[a-z0-9]+$", var.node_type))
    error_message = "node_type must look like cache.<family>.<size>, for example cache.t4g.micro."
  }
}

variable "num_cache_clusters" {
  description = "Nodes in the replication group (primary plus replicas). Null means 2 in production and 1 elsewhere. Two or more turn on automatic failover and Multi-AZ; production needs at least 2."
  type        = number
  default     = null

  validation {
    condition     = var.num_cache_clusters == null || try(var.num_cache_clusters >= 1 && var.num_cache_clusters <= 6 && floor(var.num_cache_clusters) == var.num_cache_clusters, false)
    error_message = "num_cache_clusters must be an integer between 1 and 6, or null."
  }

  validation {
    condition     = var.num_cache_clusters == null || var.environment != "production" || try(var.num_cache_clusters >= 2, false)
    error_message = "Production caches need at least 2 nodes for automatic failover."
  }
}

variable "maxmemory_policy" {
  description = "Eviction policy when memory is full (Redis maxmemory-policy)."
  type        = string
  default     = "volatile-lru"

  validation {
    condition     = contains(["volatile-lru", "allkeys-lru", "volatile-lfu", "allkeys-lfu", "volatile-random", "allkeys-random", "volatile-ttl", "noeviction"], var.maxmemory_policy)
    error_message = "maxmemory_policy must be a Redis eviction policy such as volatile-lru, allkeys-lru or noeviction."
  }
}

# ---- Authentication ----

variable "auth_mode" {
  description = "How clients authenticate. token: the module generates an AUTH token and stores it in Secrets Manager, encrypted with the module key. rbac: access is controlled by the ElastiCache user group in user_group_ids, whose users keep their passwords in Secrets Manager."
  type        = string
  default     = "token"

  validation {
    condition     = contains(["token", "rbac"], var.auth_mode)
    error_message = "auth_mode must be token or rbac."
  }
}

variable "user_group_ids" {
  description = "ElastiCache RBAC user group IDs (exactly one, as ElastiCache allows). Required when auth_mode is rbac and not allowed otherwise."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.user_group_ids) <= 1 && alltrue([for g in var.user_group_ids : can(regex("^[a-zA-Z][a-zA-Z0-9-]{0,39}$", g))])
    error_message = "user_group_ids may hold at most one user group ID (letters, digits and hyphens, starting with a letter)."
  }

  validation {
    condition     = (var.auth_mode == "rbac") == (length(var.user_group_ids) == 1)
    error_message = "auth_mode rbac requires exactly one user group ID; auth_mode token takes none."
  }
}

# ---- Protection and recovery ----

variable "snapshot_retention_days" {
  description = "Days of automatic daily snapshots. Production needs at least 1."
  type        = number
  default     = 7

  validation {
    condition     = var.snapshot_retention_days >= 0 && var.snapshot_retention_days <= 35 && floor(var.snapshot_retention_days) == var.snapshot_retention_days
    error_message = "snapshot_retention_days must be an integer between 0 and 35."
  }

  validation {
    condition     = !(var.snapshot_retention_days == 0 && var.environment == "production")
    error_message = "Production caches must keep snapshots."
  }
}

variable "log_retention_days" {
  description = "Days CloudWatch keeps the slow and engine logs."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a retention period CloudWatch Logs supports (for example 7, 14, 30, 90, 365)."
  }
}

variable "kms_key_arn" {
  description = "Existing customer-managed KMS key ARN for data at rest, logs and the AUTH token secret. If null, the module creates a dedicated key with rotation enabled. Must be known at plan time."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}
