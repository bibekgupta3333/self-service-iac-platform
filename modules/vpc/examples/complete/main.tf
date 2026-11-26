# VPC with every option exercised: a custom CIDR and subnet size, three
# zones, and NAT turned on explicitly with one gateway per zone (development
# leaves NAT off by default).

module "network" {
  source = "../../"

  name        = var.name
  team        = var.team
  environment = var.environment
  stack       = var.stack

  cidr_block     = "10.60.0.0/16"
  subnet_newbits = 4
  az_count       = 3

  enable_nat_gateway = true
  single_nat_gateway = false

  flow_log_traffic_type   = "REJECT"
  flow_log_retention_days = 14

  tags = {
    "cost-center" = "cc-1234"
  }
}
