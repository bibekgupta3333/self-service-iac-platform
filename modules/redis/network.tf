resource "aws_elasticache_subnet_group" "this" {
  name        = local.full_name
  description = "Private subnets for Redis ${local.full_name}"
  subnet_ids  = var.subnet_ids
  tags        = local.tags
}

# No inline rules: each rule is its own resource so rules change without
# replacing the group. There is no egress rule because the cache never opens
# outbound connections.
resource "aws_security_group" "this" {
  name        = "${local.full_name}-redis"
  description = "Redis ${local.full_name}: ingress only from allowed security groups"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.full_name}-redis" })
}

# count rather than for_each, so the group IDs may come from resources created
# in the same configuration. Reordering the list updates rules in place.
resource "aws_vpc_security_group_ingress_rule" "allowed" {
  count = length(var.allowed_security_group_ids)

  security_group_id            = aws_security_group.this.id
  description                  = "Redis from an allowed security group"
  ip_protocol                  = "tcp"
  from_port                    = var.port
  to_port                      = var.port
  referenced_security_group_id = var.allowed_security_group_ids[count.index]
  tags                         = local.tags
}
