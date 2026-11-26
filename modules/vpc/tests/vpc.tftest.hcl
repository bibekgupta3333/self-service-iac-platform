# terraform test suite for modules/vpc. Runs against LocalStack (ADR-0009):
#   docker compose up -d localstack && terraform -chdir=modules/vpc test

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    ec2  = "http://localhost:4566"
    iam  = "http://localhost:4566"
    kms  = "http://localhost:4566"
    logs = "http://localhost:4566"
    sts  = "http://localhost:4566"
  }
}

variables {
  name        = "tftest-vpc"
  team        = "platform"
  environment = "development"
  stack       = "tftest"
  cidr_block  = "10.42.0.0/16"
}

run "secure_defaults" {
  command = apply

  assert {
    condition     = aws_vpc.this.enable_dns_support && aws_vpc.this.enable_dns_hostnames
    error_message = "DNS support and hostnames must be enabled."
  }

  assert {
    condition     = aws_vpc.this.tags_all["Name"] == "platform-development-tftest-vpc" && output.name == "platform-development-tftest-vpc"
    error_message = "The VPC must be named <team>-<environment>-<name>."
  }

  assert {
    condition     = length(output.public_subnet_ids) == 2 && length(output.private_subnet_ids) == 2 && length(distinct(output.availability_zones)) == 2
    error_message = "Two zones with one public and one private subnet each must be created by default."
  }

  assert {
    condition = [for az in output.availability_zones : aws_subnet.public[az].cidr_block] == ["10.42.0.0/20", "10.42.16.0/20"] && [
      for az in output.availability_zones : aws_subnet.private[az].cidr_block
    ] == ["10.42.128.0/20", "10.42.144.0/20"]
    error_message = "Public subnets take the lower half of the /20 blocks and private subnets the upper half."
  }

  assert {
    condition     = alltrue([for s in concat(values(aws_subnet.public), values(aws_subnet.private)) : !s.map_public_ip_on_launch])
    error_message = "No subnet may assign public IPs on launch."
  }

  assert {
    condition     = length(aws_default_security_group.this.ingress) == 0 && length(aws_default_security_group.this.egress) == 0
    error_message = "The default security group must have no rules."
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 0 && length(aws_eip.nat) == 0 && length(aws_route.private_nat) == 0 && output.nat_gateway_ids == []
    error_message = "NAT must be off by default in development."
  }

  assert {
    condition     = aws_route.public_internet.gateway_id == aws_internet_gateway.this.id && aws_route.public_internet.destination_cidr_block == "0.0.0.0/0"
    error_message = "Public subnets must route to the internet gateway."
  }

  assert {
    condition     = output.public_route_table_ids == [aws_route_table.public.id] && length(output.private_route_table_ids) == 2 && length(distinct(output.private_route_table_ids)) == 2
    error_message = "One shared public route table and one private route table per zone must be exposed."
  }

  assert {
    condition     = alltrue([for az, a in aws_route_table_association.private : a.route_table_id == aws_route_table.private[az].id])
    error_message = "Each private subnet must use its zone's route table."
  }

  assert {
    condition     = aws_flow_log.this.vpc_id == aws_vpc.this.id && aws_flow_log.this.traffic_type == "ALL" && aws_flow_log.this.log_destination == aws_cloudwatch_log_group.flow_logs.arn
    error_message = "VPC flow logs must record all traffic to the flow log group."
  }

  assert {
    condition     = aws_cloudwatch_log_group.flow_logs.kms_key_id == aws_kms_key.this[0].arn && aws_cloudwatch_log_group.flow_logs.retention_in_days == 30
    error_message = "The flow log group must be encrypted with the module key and keep 30 days."
  }

  assert {
    condition     = aws_kms_key.this[0].enable_key_rotation
    error_message = "The module-created key must have rotation enabled."
  }

  assert {
    condition     = aws_iam_role.flow_logs.path == "/iacp/" && aws_iam_role.flow_logs.permissions_boundary == "arn:aws:iam::000000000000:policy/iacp-boundary"
    error_message = "The flow log role must live under /iacp/ with the platform boundary."
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_iam_role_policy.flow_logs.policy).Statement :
      alltrue([for r in flatten([s.Resource]) : startswith(r, aws_cloudwatch_log_group.flow_logs.arn)])
    ])
    error_message = "The flow log role may only write to its own log group."
  }

  assert {
    condition     = strcontains(aws_iam_role.flow_logs.assume_role_policy, "vpc-flow-logs.amazonaws.com") && strcontains(aws_iam_role.flow_logs.assume_role_policy, "aws:SourceAccount")
    error_message = "Only the flow log service, for this account, may assume the role."
  }

  assert {
    condition = alltrue([
      for r in [aws_vpc.this, aws_default_security_group.this, aws_internet_gateway.this, aws_flow_log.this, aws_cloudwatch_log_group.flow_logs, aws_iam_role.flow_logs, aws_kms_key.this[0]] :
      { for k, v in r.tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "vpc" }
    ])
    error_message = "VPC-level resources must carry exactly the platform tags with the stack's values."
  }

  assert {
    condition = alltrue([
      for tier, resources in { public = concat(values(aws_subnet.public), [aws_route_table.public]), private = concat(values(aws_subnet.private), values(aws_route_table.private)) } : alltrue([
        for s in resources :
        { for k, v in s.tags_all : k => v if startswith(k, "iacp:") } == { "iacp:team" = "platform", "iacp:environment" = "development", "iacp:stack" = "tftest", "iacp:managed-by" = "iacp", "iacp:module" = "vpc", "iacp:tier" = tier }
      ])
    ])
    error_message = "Subnets and route tables must carry the platform tags plus iacp:tier."
  }

  assert {
    condition     = output.vpc_id == aws_vpc.this.id && output.id == aws_vpc.this.id && output.vpc_cidr_block == "10.42.0.0/16"
    error_message = "vpc_id, id and vpc_cidr_block outputs must describe the VPC."
  }

  assert {
    condition     = output.iacp_metadata == { module = "vpc", version = "1.0.0" }
    error_message = "iacp_metadata must report the module name and version."
  }
}

run "nat_per_zone" {
  command = apply

  variables {
    name               = "tftest-vpc-nat"
    cidr_block         = "10.43.0.0/16"
    az_count           = 3
    enable_nat_gateway = true
    single_nat_gateway = false
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 3 && length(aws_eip.nat) == 3 && length(output.nat_gateway_ids) == 3
    error_message = "single_nat_gateway = false must create one NAT gateway per zone."
  }

  assert {
    condition     = alltrue([for az, r in aws_route.private_nat : r.nat_gateway_id == aws_nat_gateway.this[az].id])
    error_message = "Each private route table must route through its own zone's NAT gateway."
  }

  assert {
    condition     = alltrue([for az, n in aws_nat_gateway.this : n.subnet_id == aws_subnet.public[az].id])
    error_message = "NAT gateways must live in the public subnets."
  }

  assert {
    condition     = alltrue([for n in values(aws_nat_gateway.this) : n.tags_all["iacp:stack"] == "tftest" && n.tags_all["iacp:module"] == "vpc"])
    error_message = "NAT gateways must carry the platform tags."
  }
}

run "nat_on_outside_development" {
  command = plan

  variables {
    environment = "staging"
  }

  assert {
    condition     = length(aws_nat_gateway.this) == 1 && length(aws_route.private_nat) == 2
    error_message = "Outside development NAT must default to on, shared by default."
  }

  assert {
    condition     = alltrue([for r in values(aws_route.private_nat) : r.destination_cidr_block == "0.0.0.0/0"])
    error_message = "Private subnets must get a default route through NAT."
  }
}

run "explicit_zones_and_sizes" {
  command = plan

  variables {
    cidr_block         = "192.168.0.0/20"
    subnet_newbits     = 3
    availability_zones = ["us-east-1b", "us-east-1c"]
  }

  assert {
    condition     = tolist(local.azs) == tolist(["us-east-1b", "us-east-1c"])
    error_message = "availability_zones must be used in the given order."
  }

  assert {
    condition = [for az in local.azs : aws_subnet.public[az].cidr_block] == ["192.168.0.0/23", "192.168.2.0/23"] && [
      for az in local.azs : aws_subnet.private[az].cidr_block
    ] == ["192.168.8.0/23", "192.168.10.0/23"]
    error_message = "subnet_newbits must size the subnets and keep public and private halves apart."
  }
}

run "existing_key_and_boundary" {
  command = plan

  variables {
    kms_key_arn              = "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
    permissions_boundary_arn = "arn:aws:iam::000000000000:policy/custom-boundary"
  }

  assert {
    condition     = length(aws_kms_key.this) == 0 && aws_cloudwatch_log_group.flow_logs.kms_key_id == "arn:aws:kms:us-east-1:000000000000:key/11111111-2222-3333-4444-555555555555"
    error_message = "An existing kms_key_arn must be used instead of creating a key."
  }

  assert {
    condition     = aws_iam_role.flow_logs.permissions_boundary == "arn:aws:iam::000000000000:policy/custom-boundary"
    error_message = "An explicit permissions_boundary_arn must replace the default."
  }
}

run "rejects_shared_nat_in_production" {
  command = plan

  variables {
    environment = "production"
  }

  expect_failures = [var.single_nat_gateway]
}

run "rejects_public_cidr" {
  command = plan

  variables {
    cidr_block = "8.8.0.0/16"
  }

  expect_failures = [var.cidr_block]
}

run "rejects_host_address_cidr" {
  command = plan

  variables {
    cidr_block = "10.0.0.1/16"
  }

  expect_failures = [var.cidr_block]
}

run "rejects_too_large_cidr" {
  command = plan

  variables {
    cidr_block = "10.0.0.0/12"
  }

  expect_failures = [var.cidr_block]
}

run "rejects_too_many_zones_for_subnet_bits" {
  command = plan

  variables {
    subnet_newbits = 2
    az_count       = 3
  }

  expect_failures = [var.az_count]
}

run "rejects_subnets_smaller_than_28" {
  command = plan

  variables {
    cidr_block     = "10.0.0.0/24"
    subnet_newbits = 5
  }

  expect_failures = [var.subnet_newbits]
}

run "rejects_duplicate_zones" {
  command = plan

  variables {
    availability_zones = ["us-east-1a", "us-east-1a"]
  }

  expect_failures = [var.availability_zones]
}

run "rejects_unsupported_retention" {
  command = plan

  variables {
    flow_log_retention_days = 10
  }

  expect_failures = [var.flow_log_retention_days]
}

run "rejects_reserved_tag_prefix" {
  command = plan

  variables {
    tags = { "iacp:managed-by" = "someone" }
  }

  expect_failures = [var.tags]
}
