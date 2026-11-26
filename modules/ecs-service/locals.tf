locals {
  module_name    = "ecs-service"
  module_version = "2.0.0"

  full_name     = "${var.team}-${var.environment}-${var.name}"
  is_production = var.environment == "production"

  # Load balancer and target group names are limited to 32 characters; longer
  # names keep a readable prefix plus a stable hash of the full name.
  short_name = length(local.full_name) <= 32 ? local.full_name : "${trimsuffix(substr(local.full_name, 0, 23), "-")}-${substr(sha1(local.full_name), 0, 8)}"

  # Platform tags are merged last so user tags can never override them.
  tags = merge(var.tags, {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:module"      = local.module_name
    "iacp:managed-by"  = "iacp"
  })

  create_kms_key = var.kms_key_arn == null
  kms_key_arn    = local.create_kms_key ? aws_kms_key.this[0].arn : var.kms_key_arn

  create_cluster = var.cluster_arn == null
  cluster_arn    = local.create_cluster ? aws_ecs_cluster.this[0].arn : var.cluster_arn

  has_load_balancer = var.load_balancer != null

  # The platform boundary is always attached (SCPs deny role creation without
  # it); callers may only point at a different boundary policy.
  permissions_boundary_arn = coalesce(
    var.permissions_boundary_arn,
    "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:policy/iacp-boundary",
  )

  log_group_name = "/ecs/${local.full_name}"
  container_name = "app"
  image          = "${aws_ecr_repository.this.repository_url}:${var.image_tag}"

  secretsmanager_arns = sort([for v in values(var.secrets) : v if strcontains(v, ":secretsmanager:")])
  ssm_parameter_arns  = sort([for v in values(var.secrets) : v if strcontains(v, ":ssm:")])
}
