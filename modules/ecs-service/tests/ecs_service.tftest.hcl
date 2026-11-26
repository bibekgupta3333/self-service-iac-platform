# terraform test suite for modules/ecs-service. LocalStack community does not
# emulate ECS, ECR or ELBv2, so this suite needs no emulator at all
# (README, "Testing"):
#
#   - `command = plan` runs use the real AWS provider fully offline: dummy
#     credentials, no account lookup, every endpoint pointed at a closed local
#     port, and the account-reading data sources overridden below.
#   - `command = apply` runs use `mock_provider "aws"` (alias aws.mock) in
#     their own state, so they never call AWS.
#
#   terraform -chdir=modules/ecs-service init -backend=false && terraform -chdir=modules/ecs-service test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "mock"
  secret_key                  = "mock"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true

  # Nothing may leave the machine: any call would fail fast on a closed port.
  endpoints {
    cloudwatchlogs = "http://127.0.0.1:1"
    ec2            = "http://127.0.0.1:1"
    ecr            = "http://127.0.0.1:1"
    ecs            = "http://127.0.0.1:1"
    elbv2          = "http://127.0.0.1:1"
    iam            = "http://127.0.0.1:1"
    kms            = "http://127.0.0.1:1"
    sts            = "http://127.0.0.1:1"
  }
}

mock_provider "aws" {
  alias = "mock"

  # Generated strings are not valid JSON, which the provider's own validation
  # rejects. Statements are asserted on the data sources' blocks instead.
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_resource "aws_ecr_repository" {
    defaults = {
      arn            = "arn:aws:ecr:us-east-1:123456789012:repository/platform-development-tftest-api"
      repository_url = "123456789012.dkr.ecr.us-east-1.amazonaws.com/platform-development-tftest-api"
    }
  }

  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:us-east-1:123456789012:log-group:/ecs/platform-development-tftest-api"
    }
  }

  mock_resource "aws_kms_key" {
    defaults = {
      arn    = "arn:aws:kms:us-east-1:123456789012:key/11111111-2222-3333-4444-555555555555"
      key_id = "11111111-2222-3333-4444-555555555555"
    }
  }

  mock_resource "aws_ecs_cluster" {
    defaults = {
      arn = "arn:aws:ecs:us-east-1:123456789012:cluster/platform-development-tftest-api"
    }
  }

  mock_resource "aws_lb" {
    defaults = {
      arn      = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/platform-development-tftest-api/0123456789abcdef"
      dns_name = "internal-platform-development-tftest-api-123456.us-east-1.elb.amazonaws.com"
    }
  }

  mock_resource "aws_lb_target_group" {
    defaults = {
      arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/platform-development-tftest-api/0123456789abcdef"
    }
  }
}

override_data {
  target = data.aws_caller_identity.current
  values = {
    account_id = "123456789012"
  }
}

override_data {
  target = data.aws_partition.current
  values = {
    partition = "aws"
  }
}

override_data {
  target = data.aws_region.current
  values = {
    name = "us-east-1"
  }
}

variables {
  name        = "tftest-api"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  vpc_id      = "vpc-0123456789abcdef0"
  subnet_ids  = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
  image_tag   = "1.4.2"
  load_balancer = {
    subnet_ids          = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
    certificate_arn     = "arn:aws:acm:us-east-1:123456789012:certificate/11111111-2222-3333-4444-555555555555"
    ingress_cidr_blocks = ["10.0.0.0/16"]
  }
}

run "internal_alb_apply" {
  command = apply

  # Separate state, so the offline plan runs never refresh mocked resources.
  state_key = "mock"

  providers = {
    aws = aws.mock
  }

  variables {
    secrets = {
      DATABASE_SECRET = "arn:aws:secretsmanager:us-east-1:123456789012:secret:rds!db-orders-AbCdEf"
      FEATURE_FLAGS   = "arn:aws:ssm:us-east-1:123456789012:parameter/orders/flags"
    }
    secrets_kms_key_arns = ["arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]
    environment_variables = {
      LOG_LEVEL = "info"
    }
    egress_security_group_rules = [
      { description = "orders database", port = 5432, security_group_id = "sg-0db0000000000000a" },
    ]
  }

  override_resource {
    target = aws_security_group.service
    values = {
      id = "sg-0service000000001"
    }
  }

  override_resource {
    target = aws_security_group.alb
    values = {
      id = "sg-0alb0000000000001"
    }
  }

  # The task definition validates role ARNs, so the mocked roles need real ones.
  override_resource {
    target = aws_iam_role.execution
    values = {
      arn = "arn:aws:iam::123456789012:role/iacp/platform-development-tftest-api-execution"
    }
  }

  override_resource {
    target = aws_iam_role.task
    values = {
      arn = "arn:aws:iam::123456789012:role/iacp/platform-development-tftest-api-task"
    }
  }

  # ---- ECR ----

  assert {
    condition     = aws_ecr_repository.this.name == "platform-development-tftest-api" && aws_ecr_repository.this.image_tag_mutability == "IMMUTABLE"
    error_message = "The repository must be named after the service and use immutable tags."
  }

  assert {
    condition     = one(aws_ecr_repository.this.image_scanning_configuration).scan_on_push == true
    error_message = "Every pushed image must be scanned."
  }

  assert {
    condition     = one(aws_ecr_repository.this.encryption_configuration).encryption_type == "KMS" && one(aws_ecr_repository.this.encryption_configuration).kms_key == aws_kms_key.this[0].arn
    error_message = "Images must be encrypted with the module-created customer-managed key."
  }

  assert {
    condition     = aws_ecr_repository.this.force_delete == false
    error_message = "A repository holding images must never be force-deleted."
  }

  assert {
    condition = toset([for r in jsondecode(aws_ecr_lifecycle_policy.this.policy).rules : "${r.selection.tagStatus}/${r.selection.countType}/${r.selection.countNumber}"]) == toset([
      "untagged/sinceImagePushed/7", "any/imageCountMoreThan/30",
    ])
    error_message = "The lifecycle policy must expire untagged images after 7 days and keep 30 images."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation == true
    error_message = "The module-created key must rotate."
  }

  # ---- Task definition ----

  assert {
    condition     = aws_ecs_task_definition.this.requires_compatibilities == toset(["FARGATE"]) && aws_ecs_task_definition.this.network_mode == "awsvpc"
    error_message = "Tasks must run on Fargate with awsvpc networking."
  }

  assert {
    condition     = aws_ecs_task_definition.this.execution_role_arn == aws_iam_role.execution.arn && aws_ecs_task_definition.this.task_role_arn == aws_iam_role.task.arn
    error_message = "The task must use the module's execution and task roles."
  }

  assert {
    condition = alltrue([
      for c in jsondecode(aws_ecs_task_definition.this.container_definitions) :
      c.readonlyRootFilesystem == true && c.privileged == false && c.user == "1000:1000" && c.linuxParameters.capabilities.drop == ["ALL"]
    ])
    error_message = "The container must run as non-root with a read-only root filesystem, no privileges and all capabilities dropped."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].image == "123456789012.dkr.ecr.us-east-1.amazonaws.com/platform-development-tftest-api:1.4.2"
    error_message = "The container must run the requested tag from the service's own repository."
  }

  assert {
    condition = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].secrets == [
      { name = "DATABASE_SECRET", valueFrom = "arn:aws:secretsmanager:us-east-1:123456789012:secret:rds!db-orders-AbCdEf" },
      { name = "FEATURE_FLAGS", valueFrom = "arn:aws:ssm:us-east-1:123456789012:parameter/orders/flags" },
    ]
    error_message = "Secrets must be injected by reference, never as plain environment values."
  }

  assert {
    condition     = jsondecode(aws_ecs_task_definition.this.container_definitions)[0].logConfiguration.options["awslogs-group"] == "/ecs/platform-development-tftest-api"
    error_message = "Container logs must go to the module log group."
  }

  assert {
    condition     = aws_cloudwatch_log_group.this.kms_key_id == aws_kms_key.this[0].arn && aws_cloudwatch_log_group.this.retention_in_days == 30
    error_message = "The log group must be encrypted with the module key and keep 30 days."
  }

  # ---- Service ----

  assert {
    condition     = aws_ecs_service.this.launch_type == "FARGATE" && aws_ecs_service.this.cluster == aws_ecs_cluster.this[0].arn
    error_message = "The service must run on Fargate in the module-created cluster."
  }

  assert {
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip == false && one(aws_ecs_service.this.network_configuration).security_groups == toset(["sg-0service000000001"])
    error_message = "Tasks must never get a public IP and must use only the service security group."
  }

  assert {
    condition     = one(aws_ecs_service.this.deployment_circuit_breaker).enable == true && one(aws_ecs_service.this.deployment_circuit_breaker).rollback == true
    error_message = "The deployment circuit breaker must roll back failed deployments."
  }

  assert {
    condition     = aws_ecs_service.this.enable_execute_command == false
    error_message = "ECS Exec must stay off."
  }

  assert {
    condition     = one([for s in aws_ecs_cluster.this[0].setting : s.value if s.name == "containerInsights"]) == "enabled"
    error_message = "The module-created cluster must enable Container Insights."
  }

  # ---- Network ----

  assert {
    condition = (
      length(aws_vpc_security_group_ingress_rule.service_from_alb) == 1 &&
      aws_vpc_security_group_ingress_rule.service_from_alb[0].referenced_security_group_id == "sg-0alb0000000000001" &&
      aws_vpc_security_group_ingress_rule.service_from_alb[0].cidr_ipv4 == null &&
      aws_vpc_security_group_ingress_rule.service_from_alb[0].from_port == 8080
    )
    error_message = "The service security group may only admit the container port from the load balancer group."
  }

  assert {
    condition = (
      length(aws_vpc_security_group_egress_rule.service_to_groups) == 1 &&
      aws_vpc_security_group_egress_rule.service_to_groups[0].referenced_security_group_id == "sg-0db0000000000000a" &&
      aws_vpc_security_group_egress_rule.service_to_groups[0].from_port == 5432
    )
    error_message = "Extra egress must go to the listed security groups on their ports only."
  }

  assert {
    condition     = alltrue([for r in aws_vpc_security_group_egress_rule.service_https : r.from_port == 443 && r.to_port == 443])
    error_message = "CIDR egress from the tasks is limited to HTTPS."
  }

  assert {
    condition     = toset([for r in aws_vpc_security_group_ingress_rule.alb : "${r.from_port}/${r.cidr_ipv4}"]) == toset(["443/10.0.0.0/16", "80/10.0.0.0/16"])
    error_message = "The load balancer admits only 443 and 80 from the allowed ranges."
  }

  # ---- Load balancer ----

  assert {
    condition     = aws_lb.this[0].internal == true && aws_lb.this[0].drop_invalid_header_fields == true && aws_lb.this[0].enable_deletion_protection == false
    error_message = "The default load balancer is internal, drops invalid headers and is not deletion-protected in development."
  }

  assert {
    condition = (
      aws_lb_listener.https[0].protocol == "HTTPS" && aws_lb_listener.https[0].port == 443 &&
      aws_lb_listener.https[0].certificate_arn == var.load_balancer.certificate_arn &&
      aws_lb_listener.https[0].ssl_policy == "ELBSecurityPolicy-TLS13-1-2-2021-06"
    )
    error_message = "The HTTPS listener must use the given certificate and a TLS 1.2+ policy."
  }

  assert {
    condition = (
      aws_lb_listener.http[0].port == 80 &&
      one(aws_lb_listener.http[0].default_action).type == "redirect" &&
      one(one(aws_lb_listener.http[0].default_action).redirect).protocol == "HTTPS" &&
      one(one(aws_lb_listener.http[0].default_action).redirect).status_code == "HTTP_301"
    )
    error_message = "Port 80 must only redirect to HTTPS."
  }

  assert {
    condition     = aws_lb_target_group.this[0].target_type == "ip" && one(aws_lb_target_group.this[0].health_check).path == "/health"
    error_message = "The target group must register task IPs and probe the health check path."
  }

  # ---- IAM ----

  assert {
    condition = alltrue([
      for r in [aws_iam_role.execution, aws_iam_role.task] :
      r.path == "/iacp/" && r.permissions_boundary == "arn:aws:iam::123456789012:policy/iacp-boundary"
    ])
    error_message = "Both roles must live under /iacp/ with the platform permissions boundary."
  }

  assert {
    condition = toset(flatten([for s in data.aws_iam_policy_document.execution.statement : s.actions])) == toset([
      "ecr:GetAuthorizationToken", "ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
      "logs:CreateLogStream", "logs:PutLogEvents", "secretsmanager:GetSecretValue", "ssm:GetParameters", "kms:Decrypt",
    ])
    error_message = "The execution role must get exactly image pull, log writes and the listed secrets."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.execution.statement :
      s.sid == "EcrAuthorizationToken" || !contains(s.resources, "*")
    ])
    error_message = "Only ecr:GetAuthorizationToken may use Resource \"*\"."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.execution.statement :
      s.sid == "ReadInjectedSecrets" && s.resources == toset(["arn:aws:secretsmanager:us-east-1:123456789012:secret:rds!db-orders-AbCdEf"])
    ])
    error_message = "Secrets Manager access must be limited to the injected secrets."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.execution.statement :
      s.sid == "PullOwnRepository" && s.resources == toset([aws_ecr_repository.this.arn])
    ])
    error_message = "Image pulls must be limited to the service's own repository."
  }

  assert {
    condition     = length(aws_iam_role_policy.task_extra) == 0
    error_message = "The task role has no access unless extra_policy_jsons grants it."
  }

  # ---- Tags and outputs ----

  assert {
    condition = alltrue([
      for r in [
        aws_ecr_repository.this, aws_ecs_cluster.this[0], aws_ecs_task_definition.this, aws_ecs_service.this,
        aws_iam_role.execution, aws_iam_role.task, aws_cloudwatch_log_group.this, aws_kms_key.this[0],
        aws_security_group.service, aws_security_group.alb[0], aws_lb.this[0], aws_lb_target_group.this[0],
        aws_lb_listener.https[0], aws_lb_listener.http[0],
      ] :
      r.tags["iacp:team"] == "platform" && r.tags["iacp:environment"] == "development" && r.tags["iacp:stack"] == "tftest" && r.tags["iacp:managed-by"] == "iacp" && r.tags["iacp:module"] == "ecs-service"
    ])
    error_message = "Every taggable resource must carry the mandatory iacp:* tags."
  }

  assert {
    condition = (
      output.service_name == "platform-development-tftest-api" &&
      output.cluster_arn == aws_ecs_cluster.this[0].arn &&
      output.alb_dns_name == aws_lb.this[0].dns_name &&
      output.service_url == "https://${aws_lb.this[0].dns_name}" &&
      output.ecr_repository_url == aws_ecr_repository.this.repository_url &&
      output.security_group_id == "sg-0service000000001"
    )
    error_message = "Outputs must expose the service name, cluster ARN, load balancer DNS name, ECR URL and security group."
  }

  assert {
    condition     = output.iacp_metadata == { module = "ecs-service", version = "2.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "plan_tags_and_trust" {
  command = plan

  assert {
    condition = alltrue([
      for s in jsondecode(data.aws_iam_policy_document.assume.json).Statement :
      s.Principal.Service == "ecs-tasks.amazonaws.com" &&
      s.Condition.StringEquals["aws:SourceAccount"] == "123456789012" &&
      s.Condition.ArnLike["aws:SourceArn"] == "arn:aws:ecs:us-east-1:123456789012:*"
    ])
    error_message = "Only ECS tasks of the owning account may assume the roles."
  }

  assert {
    condition = anytrue([
      for s in jsondecode(data.aws_iam_policy_document.kms[0].json).Statement :
      s.Sid == "CloudWatchLogsForServiceLogGroup" && s.Condition.ArnEquals["kms:EncryptionContext:aws:logs:arn"] == "arn:aws:logs:us-east-1:123456789012:log-group:/ecs/platform-development-tftest-api"
    ])
    error_message = "The Logs service may use the key only for this service's log group."
  }

  assert {
    condition     = aws_ecs_service.this.tags_all["iacp:stack"] == "tftest" && aws_ecr_repository.this.tags_all["iacp:module"] == "ecs-service"
    error_message = "Planned tags_all must carry the platform tags."
  }

  assert {
    condition     = aws_lb.this[0].name == "platform-development-tftest-api"
    error_message = "Names of up to 32 characters are used as-is for the load balancer."
  }
}

run "long_names_are_shortened_for_the_load_balancer" {
  command = plan

  variables {
    name = "order-history-public-api"
  }

  assert {
    condition     = length(aws_lb.this[0].name) <= 32 && length(aws_lb_target_group.this[0].name) <= 32 && startswith(aws_lb.this[0].name, "platform-development-or")
    error_message = "Load balancer and target group names must fit in 32 characters and keep a readable prefix."
  }
}

run "public_alb_in_production" {
  command = plan

  variables {
    environment = "production"
    cluster_arn = "arn:aws:ecs:us-east-1:123456789012:cluster/shared"
    kms_key_arn = "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    load_balancer = {
      internal            = false
      subnet_ids          = ["subnet-0ccccccccccccccc3", "subnet-0ddddddddddddddd4"]
      certificate_arn     = "arn:aws:acm:us-east-1:123456789012:certificate/11111111-2222-3333-4444-555555555555"
      ingress_cidr_blocks = ["0.0.0.0/0"]
      ssl_policy          = "ELBSecurityPolicy-TLS13-1-3-2021-06"
    }
  }

  assert {
    condition     = aws_lb.this[0].internal == false && aws_lb.this[0].enable_deletion_protection == true
    error_message = "A production load balancer must be deletion-protected."
  }

  assert {
    condition     = aws_lb_listener.https[0].ssl_policy == "ELBSecurityPolicy-TLS13-1-3-2021-06"
    error_message = "ssl_policy must flow into the HTTPS listener."
  }

  assert {
    condition     = length(aws_ecs_cluster.this) == 0 && aws_ecs_service.this.cluster == var.cluster_arn
    error_message = "A supplied cluster_arn must be used instead of creating a cluster."
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && aws_cloudwatch_log_group.this.kms_key_id == var.kms_key_arn && one(aws_ecr_repository.this.encryption_configuration).kms_key == var.kms_key_arn
    error_message = "A supplied kms_key_arn replaces the module key everywhere."
  }

  assert {
    condition     = one(aws_ecs_service.this.network_configuration).assign_public_ip == false
    error_message = "Tasks never get a public IP, even behind a public load balancer."
  }
}

run "worker_without_load_balancer" {
  command = plan

  variables {
    load_balancer = null
  }

  assert {
    condition = (
      length(aws_lb.this) == 0 && length(aws_lb_listener.https) == 0 && length(aws_lb_listener.http) == 0 &&
      length(aws_security_group.alb) == 0 && length(aws_vpc_security_group_ingress_rule.service_from_alb) == 0
    )
    error_message = "Without a load balancer nothing may reach the tasks."
  }

  assert {
    condition     = length(aws_ecs_service.this.load_balancer) == 0 && aws_ecs_service.this.health_check_grace_period_seconds == null
    error_message = "A worker service has no load balancer attachment."
  }

  assert {
    condition     = output.alb_dns_name == null && output.service_url == null
    error_message = "Load balancer outputs are null for a worker."
  }
}

run "custom_boundary_and_task_policies" {
  command = plan

  variables {
    permissions_boundary_arn = "arn:aws:iam::123456789012:policy/custom-boundary"
    extra_policy_jsons = {
      read = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["s3:Get*"], Resource = "arn:aws:s3:::bucket/*" }] })
    }
  }

  assert {
    condition     = aws_iam_role.task.permissions_boundary == var.permissions_boundary_arn && aws_iam_role.execution.permissions_boundary == var.permissions_boundary_arn
    error_message = "An explicit permissions_boundary_arn must replace the default on both roles."
  }

  assert {
    condition     = keys(aws_iam_role_policy.task_extra) == ["read"] && aws_iam_role_policy.task_extra["read"].name == "extra-read"
    error_message = "Extra policy documents attach to the task role under their stable names."
  }
}

run "rejects_latest_tag" {
  command = plan

  variables {
    image_tag = "latest"
  }

  expect_failures = [var.image_tag]
}

run "rejects_root_user" {
  command = plan

  variables {
    container_user = "0:0"
  }

  expect_failures = [var.container_user]
}

run "rejects_privileged_port" {
  command = plan

  variables {
    container_port = 80
  }

  expect_failures = [var.container_port]
}

run "rejects_invalid_fargate_size" {
  command = plan

  variables {
    cpu    = 256
    memory = 4096
  }

  expect_failures = [var.memory]
}

run "rejects_non_acm_certificate" {
  command = plan

  variables {
    load_balancer = {
      subnet_ids          = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
      certificate_arn     = "arn:aws:iam::123456789012:server-certificate/legacy"
      ingress_cidr_blocks = ["10.0.0.0/16"]
    }
  }

  expect_failures = [var.load_balancer]
}

run "rejects_legacy_tls_policy" {
  command = plan

  variables {
    load_balancer = {
      subnet_ids          = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
      certificate_arn     = "arn:aws:acm:us-east-1:123456789012:certificate/11111111-2222-3333-4444-555555555555"
      ingress_cidr_blocks = ["10.0.0.0/16"]
      ssl_policy          = "ELBSecurityPolicy-2016-08"
    }
  }

  expect_failures = [var.load_balancer]
}

run "rejects_single_subnet_alb" {
  command = plan

  variables {
    load_balancer = {
      subnet_ids          = ["subnet-0aaaaaaaaaaaaaaa1"]
      certificate_arn     = "arn:aws:acm:us-east-1:123456789012:certificate/11111111-2222-3333-4444-555555555555"
      ingress_cidr_blocks = ["10.0.0.0/16"]
    }
  }

  expect_failures = [var.load_balancer]
}

run "rejects_plain_value_as_secret" {
  command = plan

  variables {
    secrets = { DB_PASSWORD = "hunter2" }
  }

  expect_failures = [var.secrets]
}

run "rejects_wildcard_task_policy" {
  command = plan

  variables {
    extra_policy_jsons = { all = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = "s3:*", Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_pass_role_task_policy" {
  command = plan

  variables {
    extra_policy_jsons = { pass = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = "iam:PassRole", Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_lowercase_pass_role_task_policy" {
  command = plan

  variables {
    extra_policy_jsons = { pass = jsonencode({ Version = "2012-10-17", Statement = { Effect = "Allow", Action = ["s3:GetObject", "iam:passrole"], Resource = ["arn:aws:s3:::b/*"] } }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_action_on_all_resources" {
  command = plan

  variables {
    extra_policy_jsons = { iam-read = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:GetRole"], Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "accepts_scoped_iam_read_and_deny_pass_role" {
  command = plan

  variables {
    extra_policy_jsons = {
      iam-read = jsonencode({ Version = "2012-10-17", Statement = [
        { Effect = "Allow", Action = ["iam:GetRole"], Resource = "arn:aws:iam::123456789012:role/iacp/orders-*" },
        { Effect = "Deny", Action = "iam:PassRole", Resource = "*" },
      ] })
    }
  }

  assert {
    condition     = keys(aws_iam_role_policy.task_extra) == ["iam-read"]
    error_message = "Scoped IAM reads and Deny statements must be accepted."
  }
}

run "rejects_pass_role_question_mark_wildcard" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:Pass?ole"], Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_pass_role_leading_question_mark" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:?assRole"], Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_pass_role_question_mark_service" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["i?m:PassRole"], Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_pass_role_prefix_wildcard" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:Pass*"], Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_pass_role_regex_spelling" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:(Pass)Role"], Resource = "arn:aws:iam::123456789012:role/iacp/admin" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_on_account_wildcard_arn" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:GetRole"], Resource = "arn:aws:iam::*:*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_on_wildcard_account" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:GetRole"], Resource = "arn:aws:iam::*:role/x" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_on_account_all_resources" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:GetRole"], Resource = "arn:aws:iam::123456789012:*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_on_all_resources_service_question_mark" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["i?m:GetRole"], Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_on_all_resources_prefix" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:Get*"], Resource = "*" }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "rejects_iam_read_without_resources" {
  command = plan

  variables {
    extra_policy_jsons = { bad = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["iam:GetRole"] }] }) }
  }

  expect_failures = [var.extra_policy_jsons]
}

run "accepts_scoped_iam_read_with_question_mark_resource" {
  command = plan

  variables {
    extra_policy_jsons = {
      iam-read = jsonencode({ Version = "2012-10-17", Statement = [
        { Effect = "Allow", Action = ["iam:GetRole", "iam:List*"], Resource = ["arn:aws:iam::123456789012:role/iacp/orders-*", "arn:aws:iam::123456789012:role/iacp/team-?"] },
        { Effect = "Deny", Action = "iam:Pass?ole", Resource = "*" },
      ] })
    }
  }

  assert {
    condition     = keys(aws_iam_role_policy.task_extra) == ["iam-read"]
    error_message = "Scoped IAM reads and Deny statements must be accepted."
  }
}

run "rejects_reserved_env_var_prefix" {
  command = plan

  variables {
    environment_variables = { ECS_ENABLE_CONTAINER_METADATA = "true" }
  }

  expect_failures = [var.environment_variables]
}

run "rejects_invalid_boundary" {
  command = plan

  variables {
    permissions_boundary_arn = "iacp-boundary"
  }

  expect_failures = [var.permissions_boundary_arn]
}

run "rejects_trailing_hyphen_team" {
  command = plan

  variables {
    team = "payments-"
  }

  expect_failures = [var.team]
}

run "rejects_double_hyphen_team" {
  command = plan

  variables {
    team = "pay--ments"
  }

  expect_failures = [var.team]
}
