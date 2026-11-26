# modules/vpc

VPC with public and private subnets across availability zones, optional NAT
egress, a locked-down default security group and KMS-encrypted flow logs.
Part of the iacp module registry (see
[docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "network" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/vpc?ref=modules/vpc/v1.0.0"

  name        = "core"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "network"      # injected by the platform

  cidr_block = "10.20.0.0/16"
  az_count   = 3
}
```

Downstream modules take `vpc_id`, `private_subnet_ids` and
`public_subnet_ids`. The module declares no provider. The calling root
configures `provider "aws"`. [`examples/complete`](examples/complete) shows a
LocalStack configuration.

## Secure defaults

| Control | Implementation |
|---|---|
| Private by default | No subnet assigns public IPs on launch (`map_public_ip_on_launch = false`), public subnets included. Private subnets have no route to the internet gateway. |
| Default security group | Taken over with `aws_default_security_group` and stripped of every rule, so a resource placed in it by mistake can neither receive nor send traffic. |
| Flow logs | Always on for the whole VPC (`ALL` traffic by default) to a CloudWatch log group encrypted with a customer managed key (dedicated key with rotation unless `kms_key_arn` is set), 30-day retention by default. |
| Least privilege | The flow log role lives under `/iacp/`, always carries a permissions boundary (default `iacp-boundary`), may be assumed only by `vpc-flow-logs.amazonaws.com` for this account, and may only write to its own log group. |
| Cost | NAT is off in development unless `enable_nat_gateway = true`, and on elsewhere. One shared NAT gateway by default; production must use one per zone. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last on every taggable resource. Subnets and route tables also carry `iacp:tier = public` or `private`. |

## Addressing

The VPC CIDR is split into `2^subnet_newbits` equal blocks. Zone `i` gets
block `i` as its public subnet and block `i + 2^(subnet_newbits - 1)` as its
private subnet. With the default `10.0.0.0/16` and `subnet_newbits = 4`, the
first two zones get `10.0.0.0/20` and `10.0.16.0/20` (public) and
`10.0.128.0/20` and `10.0.144.0/20` (private). Subnets are keyed by zone name,
so adding a zone never renumbers the others.

Zones are the first `az_count` available zones in name order, unless
`availability_zones` lists them explicitly.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical VPC name. The final name is `<team>-<environment>-<name>`. | `string` | n/a | yes |
| `team` | Owning team slug. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `cidr_block` | Private (RFC 1918) network address from /16 to /24. | `string` | `"10.0.0.0/16"` | no |
| `subnet_newbits` | Bits added to the VPC prefix per subnet (2-8; subnets no smaller than /28). | `number` | `4` | no |
| `az_count` | Zones to use (1-4, at most `2^(subnet_newbits - 1)`). Ignored with `availability_zones`. | `number` | `2` | no |
| `availability_zones` | Explicit zone names (1-4, unique) in subnet order. | `list(string)` | `[]` | no |
| `enable_nat_gateway` | NAT egress for private subnets. Null: off in development, on elsewhere. | `bool` | `null` | no |
| `single_nat_gateway` | One shared NAT gateway instead of one per zone. Must be false in production when NAT is on. | `bool` | `true` | no |
| `flow_log_traffic_type` | `ALL`, `ACCEPT` or `REJECT`. | `string` | `"ALL"` | no |
| `flow_log_retention_days` | Days CloudWatch keeps flow logs (a supported retention value). | `number` | `30` | no |
| `kms_key_arn` | Existing KMS key ARN for the flow log group. If null, a dedicated key is created. | `string` | `null` | no |
| `permissions_boundary_arn` | Boundary for the flow log role. Null uses `iacp-boundary`. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` | VPC ID. |
| `arn` | VPC ARN. |
| `name` | VPC name (`<team>-<environment>-<name>`). |
| `vpc_id` | VPC ID (same as `id`), for other modules' `vpc_id` inputs. |
| `vpc_cidr_block` | IPv4 CIDR block of the VPC. |
| `availability_zones` | Zones used, in subnet order. |
| `public_subnet_ids` | Public subnet IDs in zone order. |
| `private_subnet_ids` | Private subnet IDs in zone order. |
| `public_route_table_ids` | Route table IDs of the public subnets (one shared table). |
| `private_route_table_ids` | Route table IDs of the private subnets, one per zone, in zone order. |
| `nat_gateway_ids` | NAT gateway IDs (empty when NAT is off). |
| `default_security_group_id` | ID of the default security group, which has no rules. |
| `flow_log_group_name` | CloudWatch log group receiving the flow logs. |
| `kms_key_arn` | ARN of the KMS key encrypting the flow log group. |
| `iacp_metadata` | `{ module = "vpc", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_vpc`, `aws_default_security_group`, `aws_subnet` (public and private per
zone), `aws_internet_gateway`, `aws_route_table` (one public, one private per
zone) with routes and associations, optional `aws_eip` + `aws_nat_gateway`
(one, or one per zone), `aws_flow_log`, `aws_cloudwatch_log_group`,
`aws_iam_role` + `aws_iam_role_policy`, and optional `aws_kms_key` +
`aws_kms_alias`.

## Testing

Tests run against LocalStack (ADR-0009):

```bash
docker compose up -d localstack
terraform -chdir=modules/vpc init -backend=false
terraform -chdir=modules/vpc test
cd tests && go test -run TestVPC ./...
```
