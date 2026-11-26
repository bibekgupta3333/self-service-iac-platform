# ---- Standard inputs (docs/04-module-registry.md section 3.1) ----

variable "name" {
  description = "Logical service name. The service, cluster, repository and roles are named <team>-<environment>-<name>."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,40}$", var.name)) && !endswith(var.name, "-") && !strcontains(var.name, "--")
    error_message = "name must be 3-41 characters of lowercase letters, digits and single hyphens, starting with a letter and not ending with a hyphen."
  }

  validation {
    condition     = length("${var.team}-${var.environment}-${var.name}") <= 50
    error_message = "The computed name <team>-<environment>-<name> must be at most 50 characters (role, target group and load balancer names derive from it)."
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

# ---- Network ----

variable "vpc_id" {
  description = "VPC that holds the service, its load balancer and their security groups (for example the vpc module's id output)."
  type        = string

  validation {
    condition     = can(regex("^vpc-[0-9a-f]{8,17}$", var.vpc_id))
    error_message = "vpc_id must be a VPC ID such as vpc-0123456789abcdef0."
  }
}

variable "subnet_ids" {
  description = "Private subnets for the tasks (for example the vpc module's private_subnet_ids output). Tasks never get a public IP."
  type        = list(string)

  validation {
    condition     = length(var.subnet_ids) >= 1 && length(var.subnet_ids) <= 16 && alltrue([for s in var.subnet_ids : can(regex("^subnet-[0-9a-f]{8,17}$", s))])
    error_message = "subnet_ids must list 1-16 subnet IDs such as subnet-0123456789abcdef0."
  }
}

variable "https_egress_cidr_blocks" {
  description = "Destinations the tasks may reach on TCP 443 (ECR, CloudWatch Logs, Secrets Manager and other AWS APIs, through NAT or VPC endpoints). Narrow it to the VPC CIDR when VPC endpoints serve every API."
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = length(var.https_egress_cidr_blocks) >= 1 && length(var.https_egress_cidr_blocks) <= 20 && alltrue([for c in var.https_egress_cidr_blocks : can(cidrhost(c, 0)) && strcontains(c, ".")])
    error_message = "https_egress_cidr_blocks must list 1-20 IPv4 CIDR blocks."
  }
}

variable "egress_security_group_rules" {
  description = "Extra egress from the tasks to other security groups, for example { description = \"orders database\", port = 5432, security_group_id = <postgres security_group_id> }."
  type = list(object({
    description       = string
    port              = number
    security_group_id = string
  }))
  default = []

  validation {
    condition = length(var.egress_security_group_rules) <= 20 && alltrue([
      for r in var.egress_security_group_rules :
      can(regex("^sg-[0-9a-f]{8,17}$", r.security_group_id)) && r.port >= 1 && r.port <= 65535 && length(r.description) >= 1 && length(r.description) <= 200
    ])
    error_message = "egress_security_group_rules allows at most 20 rules, each with a security group ID, a port of 1-65535 and a 1-200 character description."
  }
}

variable "load_balancer" {
  description = <<-EOT
    Optional Application Load Balancer in front of the service. Null runs the service without one (a worker).
    internal: true (default) for a private ALB, false for an internet-facing one.
    subnet_ids: subnets for the ALB (private for internal, public for internet-facing), in at least two Availability Zones.
    certificate_arn: ACM certificate for the HTTPS listener. Port 80 only redirects to HTTPS.
    ingress_cidr_blocks: IPv4 ranges allowed to reach ports 443 and 80.
    ssl_policy: TLS policy of the HTTPS listener (TLS 1.2+ policies only).
    idle_timeout: idle connection timeout in seconds.
  EOT
  type = object({
    internal            = optional(bool, true)
    subnet_ids          = list(string)
    certificate_arn     = string
    ingress_cidr_blocks = list(string)
    ssl_policy          = optional(string, "ELBSecurityPolicy-TLS13-1-2-2021-06")
    idle_timeout        = optional(number, 60)
  })
  default = null

  validation {
    condition     = var.load_balancer == null || try(length(var.load_balancer.subnet_ids) >= 2 && alltrue([for s in var.load_balancer.subnet_ids : can(regex("^subnet-[0-9a-f]{8,17}$", s))]), false)
    error_message = "load_balancer.subnet_ids must list at least two subnet IDs."
  }

  validation {
    condition     = var.load_balancer == null || try(can(regex("^arn:aws[a-z-]*:acm:[a-z0-9-]+:[0-9]{12}:certificate/.+$", var.load_balancer.certificate_arn)), false)
    error_message = "load_balancer.certificate_arn must be an ACM certificate ARN."
  }

  validation {
    condition     = var.load_balancer == null || try(length(var.load_balancer.ingress_cidr_blocks) >= 1 && length(var.load_balancer.ingress_cidr_blocks) <= 20 && alltrue([for c in var.load_balancer.ingress_cidr_blocks : can(cidrhost(c, 0)) && strcontains(c, ".")]), false)
    error_message = "load_balancer.ingress_cidr_blocks must list 1-20 IPv4 CIDR blocks."
  }

  validation {
    condition = var.load_balancer == null || try(contains([
      "ELBSecurityPolicy-TLS13-1-2-2021-06",
      "ELBSecurityPolicy-TLS13-1-2-Res-2021-06",
      "ELBSecurityPolicy-TLS13-1-3-2021-06",
      "ELBSecurityPolicy-TLS13-1-2-FIPS-2023-04",
      "ELBSecurityPolicy-TLS13-1-3-FIPS-2023-04",
    ], var.load_balancer.ssl_policy), false)
    error_message = "load_balancer.ssl_policy must be a TLS 1.2+ policy such as ELBSecurityPolicy-TLS13-1-2-2021-06."
  }

  validation {
    condition     = var.load_balancer == null || try(var.load_balancer.idle_timeout >= 1 && var.load_balancer.idle_timeout <= 4000, false)
    error_message = "load_balancer.idle_timeout must be between 1 and 4000 seconds."
  }
}

# ---- Cluster and capacity ----

variable "cluster_arn" {
  description = "Existing ECS cluster ARN. Null creates a dedicated cluster with Container Insights enabled."
  type        = string
  default     = null

  validation {
    condition     = var.cluster_arn == null || can(regex("^arn:aws[a-z-]*:ecs:[a-z0-9-]+:[0-9]{12}:cluster/[A-Za-z0-9_-]{1,255}$", var.cluster_arn))
    error_message = "cluster_arn must be an ECS cluster ARN."
  }
}

variable "cpu" {
  description = "Task CPU units (256 = 0.25 vCPU)."
  type        = number
  default     = 256

  validation {
    condition     = contains([256, 512, 1024, 2048, 4096], var.cpu)
    error_message = "cpu must be one of 256, 512, 1024, 2048, 4096."
  }
}

variable "memory" {
  description = "Task memory in MiB. Must be a Fargate combination for the chosen cpu (256: 512-2048; 512: 1024-4096; 1024: 2048-8192; 2048: 4096-16384; 4096: 8192-30720, in 1024 steps above 512)."
  type        = number
  default     = 512

  validation {
    condition = (
      var.cpu == 256 ? contains([512, 1024, 2048], var.memory) :
      var.cpu == 512 ? var.memory >= 1024 && var.memory <= 4096 && var.memory % 1024 == 0 :
      var.cpu == 1024 ? var.memory >= 2048 && var.memory <= 8192 && var.memory % 1024 == 0 :
      var.cpu == 2048 ? var.memory >= 4096 && var.memory <= 16384 && var.memory % 1024 == 0 :
      var.memory >= 8192 && var.memory <= 30720 && var.memory % 1024 == 0
    )
    error_message = "memory must be a valid Fargate size for the chosen cpu."
  }
}

variable "architecture" {
  description = "CPU architecture of the task."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.architecture)
    error_message = "architecture must be X86_64 or ARM64."
  }
}

variable "desired_count" {
  description = "Number of tasks to keep running. 0 is allowed until the first image is pushed."
  type        = number
  default     = 1

  validation {
    condition     = var.desired_count >= 0 && var.desired_count <= 100 && floor(var.desired_count) == var.desired_count
    error_message = "desired_count must be an integer between 0 and 100."
  }
}

# ---- Container ----

variable "image_tag" {
  description = "Tag of the image in the service's ECR repository, pushed by the application's CI. Tags are immutable, so mutable tags such as latest are rejected."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$", var.image_tag)) && !contains(["latest", "main", "master"], lower(var.image_tag))
    error_message = "image_tag must be a valid Docker tag (1-128 characters) and not a moving tag such as latest, main or master."
  }
}

variable "container_port" {
  description = "Port the container listens on. Must be 1024 or higher because the container runs as a non-root user."
  type        = number
  default     = 8080

  validation {
    condition     = var.container_port >= 1024 && var.container_port <= 65535 && floor(var.container_port) == var.container_port
    error_message = "container_port must be an integer between 1024 and 65535."
  }
}

variable "container_user" {
  description = "UID[:GID] the container runs as. Root (0 or root) is rejected."
  type        = string
  default     = "1000:1000"

  validation {
    condition     = can(regex("^[0-9]{1,10}(:[0-9]{1,10})?$", var.container_user)) && !contains(["0", "0:0"], var.container_user) && !startswith(var.container_user, "0:")
    error_message = "container_user must be a numeric UID or UID:GID other than root (0)."
  }
}

variable "health_check_path" {
  description = "HTTP path the load balancer probes for a 200-399 response."
  type        = string
  default     = "/health"

  validation {
    condition     = can(regex("^/[A-Za-z0-9/_.~-]*$", var.health_check_path)) && length(var.health_check_path) <= 1024
    error_message = "health_check_path must start with / and contain only URL path characters."
  }
}

variable "environment_variables" {
  description = "Non-secret configuration for the container. Pass secrets through var.secrets, never here. Keys are letters, digits and underscores, start with a letter, and may not use the reserved AWS_ or ECS_ prefixes."
  type        = map(string)
  default     = {}

  validation {
    condition     = length(var.environment_variables) <= 50 && alltrue([for k in keys(var.environment_variables) : can(regex("^[A-Za-z][A-Za-z0-9_]*$", k)) && !startswith(k, "AWS_") && !startswith(k, "ECS_")])
    error_message = "environment_variables allows at most 50 keys of letters, digits and underscores, starting with a letter and not using the AWS_ or ECS_ prefixes."
  }
}

variable "secrets" {
  description = "Environment variables injected from Secrets Manager secrets or SSM parameters, as NAME = full ARN (for example the postgres module's secret_arn output). The execution role may read exactly these ARNs."
  type        = map(string)
  default     = {}

  validation {
    condition = length(var.secrets) <= 50 && alltrue([
      for k, v in var.secrets :
      can(regex("^[A-Za-z][A-Za-z0-9_]*$", k)) && !startswith(k, "AWS_") && !startswith(k, "ECS_") && (
        can(regex("^arn:aws[a-z-]*:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@!-]+$", v)) ||
        can(regex("^arn:aws[a-z-]*:ssm:[a-z0-9-]+:[0-9]{12}:parameter/[A-Za-z0-9/_.-]+$", v))
      )
    ])
    error_message = "secrets maps at most 50 variable names (no AWS_ or ECS_ prefix) to full Secrets Manager secret or SSM parameter ARNs."
  }

  validation {
    condition     = length(setintersection(keys(var.secrets), keys(var.environment_variables))) == 0
    error_message = "A name may not appear in both secrets and environment_variables."
  }
}

variable "secrets_kms_key_arns" {
  description = "Customer-managed KMS keys that encrypt the secrets (for example the postgres module's kms_key_arn output). The execution role gets kms:Decrypt on exactly these keys."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.secrets_kms_key_arns) <= 10 && alltrue([for k in var.secrets_kms_key_arns : can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", k))])
    error_message = "secrets_kms_key_arns allows at most 10 KMS key ARNs (aliases are not accepted)."
  }
}

# ---- IAM ----

variable "permissions_boundary_arn" {
  description = "IAM permissions boundary for the task and execution roles. Null uses the platform boundary arn:<partition>:iam::<account>:policy/iacp-boundary, which the account's SCP requires; a boundary is always attached."
  type        = string
  default     = null

  validation {
    condition     = var.permissions_boundary_arn == null || can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:policy/[A-Za-z0-9+=,.@_/-]+$", var.permissions_boundary_arn))
    error_message = "permissions_boundary_arn must be an IAM policy ARN."
  }
}

variable "extra_policy_jsons" {
  description = <<-EOT
    Additional IAM policy documents for the task role (what the application may do), keyed by a stable name (for example { orders-queue = <sqs producer_policy_json> }).
    Allow statements may not use NotAction or NotResource, and their actions may not be "*", "<service>:*", or any other wildcard except read prefixes such as s3:Get* or sqs:List*.
  EOT
  type        = map(string)
  default     = {}

  validation {
    condition     = length(var.extra_policy_jsons) <= 10 && alltrue([for k in keys(var.extra_policy_jsons) : can(regex("^[a-z0-9][a-z0-9-]{0,39}$", k))])
    error_message = "At most 10 extra policy documents, keyed by 1-40 lowercase letters, digits and hyphens."
  }

  validation {
    condition = alltrue([
      for doc in values(var.extra_policy_jsons) : try(alltrue([
        for s in flatten([jsondecode(doc).Statement]) : try(s.Effect, "") != "Allow" || (
          try(s.NotAction, null) == null &&
          try(s.NotResource, null) == null &&
          alltrue([
            for a in flatten([try(s.Action, [])]) :
            !strcontains(a, "*") || can(regex("^[A-Za-z0-9-]+:(Get|List|Describe|Head|BatchGet)[A-Za-z]*\\*$", a))
          ])
        )
      ]), false)
    ])
    error_message = "extra_policy_jsons must be valid IAM policy JSON whose Allow statements use explicit actions (no \"*\", \"<service>:*\", NotAction or NotResource)."
  }
}

# ---- Registry, logs and encryption ----

variable "ecr_max_image_count" {
  description = "Tagged images kept in the ECR repository; older ones expire. Untagged images expire after 7 days."
  type        = number
  default     = 30

  validation {
    condition     = var.ecr_max_image_count >= 1 && var.ecr_max_image_count <= 1000 && floor(var.ecr_max_image_count) == var.ecr_max_image_count
    error_message = "ecr_max_image_count must be an integer between 1 and 1000."
  }
}

variable "log_retention_days" {
  description = "Days CloudWatch keeps container logs."
  type        = number
  default     = 30

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a retention period CloudWatch Logs supports (for example 7, 14, 30, 90, 365)."
  }
}

variable "kms_key_arn" {
  description = "Existing customer-managed KMS key ARN for the ECR repository and the log group. If null, the module creates a dedicated key with rotation enabled. Must be known at plan time."
  type        = string
  default     = null

  validation {
    condition     = var.kms_key_arn == null || can(regex("^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$", var.kms_key_arn))
    error_message = "kms_key_arn must be a KMS key ARN (aliases are not accepted)."
  }
}
