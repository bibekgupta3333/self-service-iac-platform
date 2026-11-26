# Changelog — modules/ecs-service

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/ecs-service/vX.Y.Z`.

## [2.0.0] - 2026-11-26

### Changed (breaking)
- `extra_policy_jsons` closes a wildcard bypass of the `iam:PassRole` rule. IAM
  reads `?` as one character, so `iam:Pass?ole`, `iam:?assRole` and
  `i?m:PassRole` were accepted by 1.0.0 although they allow `iam:PassRole`.
  No action may now glob-match `iam:PassRole`. An action that can be an `iam:`
  action (no service part, or a service part that glob-matches `iam`) needs
  resources that name an account: `"*"`, `arn:aws:iam::*:*` and a wildcard in
  the partition, service, region or account field are rejected. Actions may
  only contain letters, digits, `:`, `-`, `_`, `*` and `?`.
  The release gate classifies a stricter validation as MAJOR because it may
  reject values that were valid before.

### Upgrade notes
- No resource changes: this release only tightens a validation (the only plan
  difference for a stack that passes it is the `iacp_metadata` output, which
  now reports version 2.0.0).
- A stack that fails the new validation at plan time has an
  `extra_policy_jsons` document with a `?` or `*` spelling of `iam:PassRole`,
  an action without a service part, or an `iam:` action whose resource is
  `"*"` or has a wildcard account. Remove the PassRole grant, and name the IAM
  ARNs the task touches, such as `arn:aws:iam::<account>:role/iacp/<team>-*`.
- Deny statements are not affected.
- Change `?ref=modules/ecs-service/v1.0.0` to `?ref=modules/ecs-service/v2.0.0`
  once the policies pass.

## [1.0.0] - 2026-11-26

### Added
- ECS Fargate service and task definition named `<team>-<environment>-<name>`
  in private subnets without public IPs, in a module-created cluster (Container
  Insights on) or `cluster_arn`.
- Deployment circuit breaker with rollback; ECS Exec off.
- Container hardening: non-root user (root rejected), read-only root
  filesystem with a writable `/tmp`, not privileged, all capabilities dropped.
- ECR repository per service: immutable tags (`latest`, `main`, `master`
  rejected as `image_tag`), scan on push, KMS encryption, a lifecycle policy
  (untagged after 7 days, newest `ecr_max_image_count` tagged images kept),
  `force_delete = false`.
- Execution and task roles under `/iacp/` with a permissions boundary (default
  `iacp-boundary`) and a trust policy scoped to the owning account. The
  execution role can pull from its repository, write its logs and read
  exactly the `secrets` ARNs and `secrets_kms_key_arns`; the task role gets
  only `extra_policy_jsons` (wildcards, `NotAction`, `NotResource`,
  `iam:PassRole` and `iam:` actions on `Resource "*"` rejected).
- CloudWatch log group with KMS and retention (default 30 days).
- Optional Application Load Balancer (internal by default): HTTPS listener
  with an ACM certificate and a TLS 1.2+ policy, HTTP-to-HTTPS redirect,
  invalid headers dropped, deletion protection in production.
- Security groups: tasks admit only the container port from the load
  balancer; egress is HTTPS to `https_egress_cidr_blocks` plus
  `egress_security_group_rules`.
- Outputs `service_name`, `cluster_arn`, `alb_dns_name`, `service_url`,
  `ecr_repository_url`, `security_group_id`, role ARNs.
- Mandatory `iacp:*` tags on every taggable resource, propagated to tasks.
- `team` and `name` reject trailing and consecutive hyphens, which would make
  `<team>-<environment>-<name>` an invalid identifier.
- Offline `terraform test` suite (real provider for plans, `mock_provider`
  for applies), because LocalStack community has no ECS, ECR or ELBv2.
