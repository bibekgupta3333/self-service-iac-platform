# Changelog — modules/ecs-service

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/ecs-service/vX.Y.Z`.

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
