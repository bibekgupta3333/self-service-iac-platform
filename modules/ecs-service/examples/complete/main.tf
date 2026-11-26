# An HTTPS API on Fargate with its database, wired the way the platform wires
# stacks: the postgres module admits only the service's security group, the
# service may reach only the database's security group (plus HTTPS to AWS
# APIs), and the RDS-managed master secret is injected by reference.
#
# The network arrives as inputs (in a platform stack, outputs of the vpc
# stack). This example creates a minimal stand-in network so it plans on its
# own. It is plan-only, because LocalStack community does not emulate ECS,
# ECR, ELBv2 or RDS (see README.md).

locals {
  platform_tags = {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:managed-by"  = "iacp"
  }
}

resource "aws_vpc" "example" {
  #checkov:skip=CKV2_AWS_11:Stand-in network for the example plan; flow logs belong to the vpc module.
  #checkov:skip=CKV2_AWS_12:Stand-in network for the example plan; the default security group belongs to the vpc module.
  cidr_block           = "10.42.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(local.platform_tags, { Name = "${var.name}-example" })
}

resource "aws_subnet" "private" {
  count = length(var.availability_zones)

  vpc_id            = aws_vpc.example.id
  cidr_block        = cidrsubnet(aws_vpc.example.cidr_block, 8, count.index)
  availability_zone = var.availability_zones[count.index]
  tags              = merge(local.platform_tags, { Name = "${var.name}-private-${count.index}" })
}

module "database" {
  source = "../../../postgres"

  name        = "${var.name}-db"
  team        = var.team
  environment = var.environment
  stack       = var.stack

  vpc_id                     = aws_vpc.example.id
  subnet_ids                 = aws_subnet.private[*].id
  allowed_security_group_ids = [module.api.security_group_id]
  database_name              = "orders"
}

module "api" {
  source = "../../"

  name        = var.name
  team        = var.team
  environment = var.environment
  stack       = var.stack

  vpc_id     = aws_vpc.example.id
  subnet_ids = aws_subnet.private[*].id

  image_tag      = var.image_tag
  cpu            = 512
  memory         = 1024
  desired_count  = 2
  container_port = 8080

  # Internal ALB: HTTPS with the given certificate, port 80 redirects.
  load_balancer = {
    internal            = true
    subnet_ids          = aws_subnet.private[*].id
    certificate_arn     = var.certificate_arn
    ingress_cidr_blocks = [aws_vpc.example.cidr_block]
  }

  environment_variables = {
    LOG_LEVEL = "info"
    DB_HOST   = module.database.endpoint
  }

  # The master secret is read by the execution role at task start; the value
  # never appears in Terraform or in the task definition.
  secrets = {
    DB_CREDENTIALS = module.database.secret_arn
  }
  secrets_kms_key_arns = [module.database.kms_key_arn]

  egress_security_group_rules = [
    { description = "orders database", port = 5432, security_group_id = module.database.security_group_id },
  ]

  tags = {
    "cost-center" = "cc-1234"
  }
}
