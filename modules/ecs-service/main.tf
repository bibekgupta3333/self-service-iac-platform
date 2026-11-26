resource "aws_ecs_cluster" "this" {
  count = local.create_cluster ? 1 : 0

  name = local.full_name

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = local.tags
}

resource "aws_ecs_task_definition" "this" {
  family                   = local.full_name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.architecture
  }

  # The root filesystem is read-only; /tmp is the only writable path.
  volume {
    name = "tmp"
  }

  container_definitions = jsonencode([
    {
      name                   = local.container_name
      image                  = local.image
      essential              = true
      user                   = var.container_user
      readonlyRootFilesystem = true
      privileged             = false
      portMappings = [{
        containerPort = var.container_port
        protocol      = "tcp"
      }]
      mountPoints = [{
        sourceVolume  = "tmp"
        containerPath = "/tmp"
        readOnly      = false
      }]
      linuxParameters = {
        initProcessEnabled = true
        capabilities       = { drop = ["ALL"] }
      }
      environment = [for k in sort(keys(var.environment_variables)) : { name = k, value = var.environment_variables[k] }]
      secrets     = [for k in sort(keys(var.secrets)) : { name = k, valueFrom = var.secrets[k] }]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this.name
          awslogs-region        = data.aws_region.current.name
          awslogs-stream-prefix = local.container_name
        }
      }
    }
  ])

  tags = local.tags
}

resource "aws_ecs_service" "this" {
  name                    = local.full_name
  cluster                 = local.cluster_arn
  task_definition         = aws_ecs_task_definition.this.arn
  desired_count           = var.desired_count
  launch_type             = "FARGATE"
  platform_version        = "LATEST"
  enable_execute_command  = false
  propagate_tags          = "SERVICE"
  enable_ecs_managed_tags = true

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  # A deployment whose tasks never become healthy is stopped and rolled back.
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [aws_security_group.service.id]
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = local.has_load_balancer ? [1] : []

    content {
      target_group_arn = aws_lb_target_group.this[0].arn
      container_name   = local.container_name
      container_port   = var.container_port
    }
  }

  health_check_grace_period_seconds = local.has_load_balancer ? 60 : null

  tags = local.tags

  # The target group must be attached to a listener before ECS registers
  # targets, and the execution role needs its permissions first.
  depends_on = [
    aws_lb_listener.https,
    aws_iam_role_policy.execution,
  ]
}
