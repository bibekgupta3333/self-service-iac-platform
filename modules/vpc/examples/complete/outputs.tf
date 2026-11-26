output "vpc_id" {
  description = "ID of the example VPC."
  value       = module.network.vpc_id
}

output "public_subnet_ids" {
  description = "Public subnet IDs."
  value       = module.network.public_subnet_ids
}

output "private_subnet_ids" {
  description = "Private subnet IDs."
  value       = module.network.private_subnet_ids
}

output "public_route_table_ids" {
  description = "Public route table IDs."
  value       = module.network.public_route_table_ids
}

output "private_route_table_ids" {
  description = "Private route table IDs."
  value       = module.network.private_route_table_ids
}

output "nat_gateway_ids" {
  description = "NAT gateway IDs."
  value       = module.network.nat_gateway_ids
}

output "default_security_group_id" {
  description = "Default security group ID."
  value       = module.network.default_security_group_id
}

output "flow_log_group_name" {
  description = "Flow log group name."
  value       = module.network.flow_log_group_name
}

output "kms_key_arn" {
  description = "KMS key encrypting the flow log group."
  value       = module.network.kms_key_arn
}
