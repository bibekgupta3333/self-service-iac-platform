# One public and one private subnet per zone, keyed by zone name so adding a
# zone never renumbers the others. No subnet assigns public IPs on launch:
# resources that need one (load balancers, NAT gateways) get it explicitly.

resource "aws_subnet" "public" {
  for_each = local.subnets

  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value.public_cidr
  map_public_ip_on_launch = false

  tags = merge(local.tags, {
    Name        = "${local.full_name}-public-${each.key}"
    "iacp:tier" = "public"
  })
}

resource "aws_subnet" "private" {
  for_each = local.subnets

  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = each.value.private_cidr
  map_public_ip_on_launch = false

  tags = merge(local.tags, {
    Name        = "${local.full_name}-private-${each.key}"
    "iacp:tier" = "private"
  })
}
