resource "aws_lb" "this" {
  #checkov:skip=CKV_AWS_91:Access logs need a log bucket with an ELB delivery policy; the s3 module does not offer one yet, so access logging is planned for a later minor release.
  #checkov:skip=CKV2_AWS_28:WAF association is an account-level decision (Firewall Manager); internal load balancers are the default.
  #checkov:skip=CKV2_AWS_20:Port 80 only answers with a 301 redirect to HTTPS (aws_lb_listener.http); Checkov does not follow the redirect.
  count = local.has_load_balancer ? 1 : 0

  name                       = local.short_name
  load_balancer_type         = "application"
  internal                   = var.load_balancer.internal
  subnets                    = var.load_balancer.subnet_ids
  security_groups            = [aws_security_group.alb[0].id]
  idle_timeout               = var.load_balancer.idle_timeout
  drop_invalid_header_fields = true
  desync_mitigation_mode     = "defensive"
  enable_deletion_protection = local.is_production
  tags                       = local.tags
}

resource "aws_lb_target_group" "this" {
  #checkov:skip=CKV_AWS_378:TLS terminates at the load balancer; traffic to the tasks stays inside the VPC and is limited by security groups to the load balancer.
  count = local.has_load_balancer ? 1 : 0

  name                 = local.short_name
  vpc_id               = var.vpc_id
  target_type          = "ip"
  protocol             = "HTTP"
  port                 = var.container_port
  deregistration_delay = 30

  health_check {
    enabled             = true
    path                = var.health_check_path
    protocol            = "HTTP"
    matcher             = "200-399"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = local.tags
}

resource "aws_lb_listener" "https" {
  count = local.has_load_balancer ? 1 : 0

  load_balancer_arn = aws_lb.this[0].arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = var.load_balancer.ssl_policy
  certificate_arn   = var.load_balancer.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this[0].arn
  }

  tags = local.tags
}

# Plain HTTP never reaches the service: it is answered with a permanent
# redirect to HTTPS.
resource "aws_lb_listener" "http" {
  #checkov:skip=CKV_AWS_2:This listener only redirects to HTTPS (301); it never forwards traffic.
  #checkov:skip=CKV_AWS_103:This listener only redirects to HTTPS (301); TLS policy applies to the HTTPS listener.
  count = local.has_load_balancer ? 1 : 0

  load_balancer_arn = aws_lb.this[0].arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"

    redirect {
      protocol    = "HTTPS"
      port        = "443"
      status_code = "HTTP_301"
    }
  }

  tags = local.tags
}
