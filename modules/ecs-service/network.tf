# Security groups carry no inline rules: each rule is its own resource so rules
# change without replacing the group.

# ---- Tasks ----

resource "aws_security_group" "service" {
  name        = "${local.full_name}-service"
  description = "ECS service ${local.full_name}: ingress only from its load balancer"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.full_name}-service" })
}

resource "aws_vpc_security_group_ingress_rule" "service_from_alb" {
  count = local.has_load_balancer ? 1 : 0

  security_group_id            = aws_security_group.service.id
  description                  = "Container port from the load balancer"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = aws_security_group.alb[0].id
  tags                         = local.tags
}

resource "aws_vpc_security_group_egress_rule" "service_https" {
  count = length(var.https_egress_cidr_blocks)

  security_group_id = aws_security_group.service.id
  description       = "HTTPS to AWS APIs (ECR, Logs, Secrets Manager)"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = var.https_egress_cidr_blocks[count.index]
  tags              = local.tags
}

resource "aws_vpc_security_group_egress_rule" "service_to_groups" {
  count = length(var.egress_security_group_rules)

  security_group_id            = aws_security_group.service.id
  description                  = var.egress_security_group_rules[count.index].description
  ip_protocol                  = "tcp"
  from_port                    = var.egress_security_group_rules[count.index].port
  to_port                      = var.egress_security_group_rules[count.index].port
  referenced_security_group_id = var.egress_security_group_rules[count.index].security_group_id
  tags                         = local.tags
}

# ---- Load balancer ----

resource "aws_security_group" "alb" {
  count = local.has_load_balancer ? 1 : 0

  name        = "${local.full_name}-alb"
  description = "Load balancer of ECS service ${local.full_name}: HTTPS (and the HTTP redirect) from allowed ranges"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.full_name}-alb" })
}

resource "aws_vpc_security_group_ingress_rule" "alb" {
  for_each = local.has_load_balancer ? {
    for pair in setproduct([443, 80], var.load_balancer.ingress_cidr_blocks) : "${pair[0]}-${pair[1]}" => { port = pair[0], cidr = pair[1] }
  } : {}

  security_group_id = aws_security_group.alb[0].id
  description       = each.value.port == 443 ? "HTTPS from an allowed range" : "HTTP (redirected to HTTPS) from an allowed range"
  ip_protocol       = "tcp"
  from_port         = each.value.port
  to_port           = each.value.port
  cidr_ipv4         = each.value.cidr
  tags              = local.tags
}

resource "aws_vpc_security_group_egress_rule" "alb_to_service" {
  count = local.has_load_balancer ? 1 : 0

  security_group_id            = aws_security_group.alb[0].id
  description                  = "Container port on the service tasks"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = aws_security_group.service.id
  tags                         = local.tags
}
