resource "aws_vpc" "this" {
  cidr_block           = var.cidr_block
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(local.tags, { Name = local.full_name })
}

# Take over the default security group and remove all of its rules, so
# anything placed in it by mistake can neither receive nor send traffic.
resource "aws_default_security_group" "this" {
  vpc_id = aws_vpc.this.id

  tags = merge(local.tags, { Name = "${local.full_name}-default-deny" })
}
