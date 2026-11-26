output "id" {
  description = "VPC ID."
  value       = aws_vpc.this.id
}

output "arn" {
  description = "VPC ARN."
  value       = aws_vpc.this.arn
}

output "name" {
  description = "VPC name (<team>-<environment>-<name>), also its Name tag."
  value       = local.full_name
}

output "vpc_id" {
  description = "VPC ID (same as id), named for wiring into other modules' vpc_id inputs."
  value       = aws_vpc.this.id
}

output "vpc_cidr_block" {
  description = "IPv4 CIDR block of the VPC."
  value       = aws_vpc.this.cidr_block
}

output "availability_zones" {
  description = "Availability zones used, in subnet order."
  value       = local.azs
}

output "public_subnet_ids" {
  description = "Public subnet IDs, in availability zone order."
  value       = [for az in local.azs : aws_subnet.public[az].id]
}

output "private_subnet_ids" {
  description = "Private subnet IDs, in availability zone order."
  value       = [for az in local.azs : aws_subnet.private[az].id]
}

output "public_route_table_ids" {
  description = "Route table IDs of the public subnets (one table shared by all of them)."
  value       = [aws_route_table.public.id]
}

output "private_route_table_ids" {
  description = "Route table IDs of the private subnets, one per availability zone, in zone order."
  value       = [for az in local.azs : aws_route_table.private[az].id]
}

output "nat_gateway_ids" {
  description = "NAT gateway IDs (empty when NAT is off)."
  value       = [for az in local.nat_azs : aws_nat_gateway.this[az].id]
}

output "default_security_group_id" {
  description = "ID of the VPC's default security group, which has no rules."
  value       = aws_default_security_group.this.id
}

output "flow_log_group_name" {
  description = "CloudWatch log group receiving the VPC flow logs."
  value       = aws_cloudwatch_log_group.flow_logs.name
}

output "kms_key_arn" {
  description = "ARN of the KMS key encrypting the flow log group."
  value       = local.kms_key_arn
}

output "iacp_metadata" {
  description = "Module identity for inventory and drift correlation."
  value       = { module = local.module_name, version = local.module_version }
}
