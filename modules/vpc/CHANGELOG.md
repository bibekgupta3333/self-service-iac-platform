# Changelog — modules/vpc

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/vpc/vX.Y.Z`.

## [1.0.0] - 2026-11-26

### Added
- VPC named `<team>-<environment>-<name>` with DNS support and hostnames, from
  a private (RFC 1918) CIDR between /16 and /24.
- One public and one private subnet per availability zone (1-4 zones, the
  first `az_count` available or an explicit `availability_zones` list), keyed
  by zone name and sized with `subnet_newbits`. No subnet assigns public IPs
  on launch.
- Internet gateway and a shared public route table; one private route table
  per zone.
- Optional NAT: off in development and on elsewhere by default, one shared
  gateway or one per zone (required in production).
- Default security group taken over with no rules.
- VPC flow logs to a CloudWatch log group encrypted with a module-created key
  (rotation on) unless `kms_key_arn` is given, delivered by a role under
  `/iacp/` with a permissions boundary that may only write to that log group.
- Mandatory `iacp:*` tags on every taggable resource, plus `iacp:tier` on
  subnets and route tables.
