# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical VPC name. The final name is <team>-<environment>-<name>."
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

# ---- Addressing ----

variable "cidr_block" {
  description = "IPv4 CIDR block of the VPC, a private (RFC 1918) range from /16 to /24."
  type        = string
  default     = "10.0.0.0/16"

  validation {
    condition = can(cidrhost(var.cidr_block, 0)) && try(
      cidrhost(var.cidr_block, 0) == split("/", var.cidr_block)[0] &&
      tonumber(split("/", var.cidr_block)[1]) >= 16 && tonumber(split("/", var.cidr_block)[1]) <= 24 &&
      anytrue([
        for range in ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"] :
        cidrhost(range, 0) == cidrhost("${cidrhost(var.cidr_block, 0)}/${split("/", range)[1]}", 0)
      ]),
      false,
    )
    error_message = "cidr_block must be a network address (for example 10.20.0.0/16) in 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, with a prefix from /16 to /24."
  }
}

variable "subnet_newbits" {
  description = "Bits added to the VPC prefix for each subnet (a /16 with 4 gives /20 subnets). Public subnets use the lower half of the resulting blocks and private subnets the upper half, so az_count may be at most 2^(subnet_newbits - 1)."
  type        = number
  default     = 4

  validation {
    condition     = var.subnet_newbits >= 2 && var.subnet_newbits <= 8 && floor(var.subnet_newbits) == var.subnet_newbits
    error_message = "subnet_newbits must be a whole number between 2 and 8."
  }

  validation {
    condition     = try(tonumber(split("/", var.cidr_block)[1]) + var.subnet_newbits <= 28, true)
    error_message = "Subnets may not be smaller than /28: the VPC prefix plus subnet_newbits must be at most 28."
  }
}

variable "az_count" {
  description = "Number of availability zones to spread subnets across (1-4). Each zone gets one public and one private subnet. Ignored when availability_zones is set."
  type        = number
  default     = 2

  validation {
    condition     = var.az_count >= 1 && var.az_count <= 4 && floor(var.az_count) == var.az_count
    error_message = "az_count must be a whole number between 1 and 4."
  }

  validation {
    condition     = length(var.availability_zones) > 0 || var.az_count <= pow(2, var.subnet_newbits - 1)
    error_message = "az_count may be at most 2^(subnet_newbits - 1); raise subnet_newbits for more zones."
  }
}

variable "availability_zones" {
  description = "Explicit availability zone names (1-4, unique), in subnet order. Empty (the default) uses the first az_count zones the region reports as available."
  type        = list(string)
  default     = []
  nullable    = false

  validation {
    condition = length(var.availability_zones) <= 4 && length(distinct(var.availability_zones)) == length(var.availability_zones) && alltrue([
      for az in var.availability_zones : can(regex("^[a-z]{2}(-gov)?-[a-z]+-[0-9][a-z]$", az))
    ])
    error_message = "availability_zones must be at most 4 unique zone names such as us-east-1a."
  }

  validation {
    condition     = length(var.availability_zones) <= pow(2, var.subnet_newbits - 1)
    error_message = "availability_zones may list at most 2^(subnet_newbits - 1) zones; raise subnet_newbits for more zones."
  }
}

# ---- Egress ----

variable "enable_nat_gateway" {
  description = "Give private subnets outbound internet access through NAT gateways. Null (the default) turns NAT off in development and on in staging and production."
  type        = bool
  default     = null

  validation {
    condition     = var.enable_nat_gateway == null || contains([true, false], var.enable_nat_gateway)
    error_message = "enable_nat_gateway must be true, false or null."
  }
}

variable "single_nat_gateway" {
  description = "Share one NAT gateway (in the first zone) between all private subnets instead of one per zone. Cheaper, but private egress then depends on one zone. Null (the default) shares one gateway in development and staging and uses one per zone in production, where true is rejected."
  type        = bool
  default     = null

  validation {
    condition     = !(var.single_nat_gateway == true && var.environment == "production" && var.enable_nat_gateway != false)
    error_message = "Production VPCs with NAT need one NAT gateway per zone, so single_nat_gateway = true is not allowed in production. Leave it null (one gateway per zone) or set it to false."
  }
}

# ---- Flow logs ----

variable "flow_log_traffic_type" {
  description = "Traffic recorded by the VPC flow log: ALL (default), ACCEPT or REJECT."
  type        = string
  default     = "ALL"

  validation {
    condition     = contains(["ALL", "ACCEPT", "REJECT"], var.flow_log_traffic_type)
    error_message = "flow_log_traffic_type must be ALL, ACCEPT or REJECT."
  }
}

variable "flow_log_retention_days" {
  description = "Days CloudWatch keeps flow log records."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.flow_log_retention_days)
    error_message = "flow_log_retention_days must be a retention period CloudWatch Logs supports (for example 7, 14, 30, 90, 365)."
  }
}

variable "kms_key_arn" {
  description = "Existing KMS key ARN for the flow log group. Its policy must let CloudWatch Logs use it. If null, the module creates a dedicated key with rotation enabled."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}

variable "permissions_boundary_arn" {
  description = "IAM permissions boundary for the flow log delivery role. Null uses the platform boundary arn:<partition>:iam::<account>:policy/iacp-boundary; a boundary is always attached."
  type        = string
  default     = null

  validation {
    condition     = var.permissions_boundary_arn == null || can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:policy/[A-Za-z0-9+=,.@_/-]+$", var.permissions_boundary_arn))
    error_message = "permissions_boundary_arn must be an IAM policy ARN."
  }
}
