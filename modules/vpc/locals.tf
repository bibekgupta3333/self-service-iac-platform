locals {
  module_name    = "vpc"
  module_version = "1.0.0"

  full_name      = "${var.team}-${var.environment}-${var.name}"
  is_production  = var.environment == "production"
  flow_log_group = "/iacp/vpc-flow-logs/${local.full_name}"

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  # Zones in subnet order: explicit names, or the first az_count available ones.
  # The data source's postconditions reject an az_count the region cannot meet;
  # min() only stops slice() from failing first with a bare index error.
  azs = length(var.availability_zones) > 0 ? var.availability_zones : slice(
    sort(data.aws_availability_zones.available.names), 0, min(var.az_count, length(data.aws_availability_zones.available.names)),
  )

  # Subnet plan: zone i gets block i (public) and block i + half (private) of
  # the VPC split into 2^subnet_newbits blocks. Pure function of the inputs.
  private_offset = pow(2, var.subnet_newbits - 1)
  subnets = {
    for i, az in local.azs : az => {
      index        = i
      public_cidr  = cidrsubnet(var.cidr_block, var.subnet_newbits, i)
      private_cidr = cidrsubnet(var.cidr_block, var.subnet_newbits, i + local.private_offset)
    }
  }

  # NAT: off in development unless asked for; one shared gateway in the first
  # zone (development and staging by default), or one per zone (production).
  nat_enabled = coalesce(var.enable_nat_gateway, var.environment != "development")
  single_nat  = coalesce(var.single_nat_gateway, !local.is_production)
  nat_azs     = local.nat_enabled ? (local.single_nat ? [local.azs[0]] : local.azs) : []

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn

  # The platform boundary is always attached to the flow log role.
  permissions_boundary_arn = coalesce(
    var.permissions_boundary_arn,
    "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:policy/iacp-boundary",
  )
}
