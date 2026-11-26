# A private PostgreSQL instance wired the way the platform wires stacks: the
# network and the client security group arrive as inputs (in a platform stack
# they are outputs of the vpc and ecs-service stacks). This example creates a
# minimal stand-in network so it plans on its own; it is plan-only, because
# LocalStack community does not emulate RDS (see README.md).

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
  cidr_block           = "10.40.0.0/16"
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

# Stand-in for the application's security group (ecs-service or lambda).
resource "aws_security_group" "app" {
  #checkov:skip=CKV2_AWS_5:Stand-in client group for the example; it is attached by the application stack.
  name        = "${var.name}-app"
  description = "Example application allowed to reach the database"
  vpc_id      = aws_vpc.example.id
  tags        = local.platform_tags
}

module "database" {
  source = "../../"

  name        = var.name
  team        = var.team
  environment = var.environment
  stack       = var.stack

  vpc_id                     = aws_vpc.example.id
  subnet_ids                 = aws_subnet.private[*].id
  allowed_security_group_ids = [aws_security_group.app.id]

  engine_version           = "16"
  instance_class           = "db.t4g.micro"
  allocated_storage_gb     = 20
  max_allocated_storage_gb = 100
  database_name            = "orders"

  tags = {
    "cost-center" = "cc-1234"
  }
}
