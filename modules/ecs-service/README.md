# modules/ecs-service

ECS Fargate service with its own ECR repository, least-privilege task and
execution roles, encrypted logs, deployment rollback and an optional HTTPS
Application Load Balancer. Part of the iacp module registry (see
[docs/04-module-registry.md](../../docs/04-module-registry.md)).

## Usage

```hcl
module "orders_api" {
  source = "git::https://github.com/bibekgupta3333/self-service-iac-platform.git//modules/ecs-service?ref=modules/ecs-service/v1.0.0"

  name        = "orders-api"
  team        = "payments"     # injected by the platform
  environment = "development"  # injected by the platform
  stack       = "orders"       # injected by the platform

  # Wired from the outputs of the vpc stack.
  vpc_id     = "vpc-0123456789abcdef0"
  subnet_ids = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]

  image_tag      = "1.4.2"   # pushed by CI to ecr_repository_url
  cpu            = 512
  memory         = 1024
  container_port = 8080

  load_balancer = {
    internal            = true
    subnet_ids          = ["subnet-0aaaaaaaaaaaaaaa1", "subnet-0bbbbbbbbbbbbbbb2"]
    certificate_arn     = "arn:aws:acm:us-east-1:123456789012:certificate/1111aaaa-22bb-33cc-44dd-555555eeeeee"
    ingress_cidr_blocks = ["10.0.0.0/16"]
  }

  # Wired from the outputs of a postgres stack.
  secrets              = { DB_CREDENTIALS = var.orders_db_secret_arn }
  secrets_kms_key_arns = [var.orders_db_kms_key_arn]
  egress_security_group_rules = [
    { description = "orders database", port = 5432, security_group_id = var.orders_db_security_group_id },
  ]
}
```

The first apply creates the repository; CI then pushes
`<ecr_repository_url>:<image_tag>`. Use `desired_count = 0` until the first
image exists. Give `security_group_id` to the postgres or redis module's
`allowed_security_group_ids`.

The module declares no provider. The calling root configures `provider "aws"`.
[`examples/complete`](examples/complete) plans an API behind an internal ALB
together with its postgres database.

## Secure defaults

| Control | Implementation |
|---|---|
| Image supply chain | One ECR repository per service with `IMMUTABLE` tags (moving tags such as `latest` are rejected as `image_tag`), scan on push, KMS encryption with the module key, `force_delete = false`, and a lifecycle policy (untagged images expire after 7 days, the newest 30 tagged images are kept). |
| Runtime | Fargate only, `awsvpc`, no public IP. The container runs as a non-root user (default `1000:1000`; root is rejected) with a read-only root filesystem (writable `/tmp` volume), not privileged, all Linux capabilities dropped, `initProcessEnabled`. ECS Exec is off. |
| Least privilege | Execution role: `ecr:GetAuthorizationToken` (the only `*` resource, an API limitation), image pull from its own repository, log writes to its own log group, and `GetSecretValue`/`GetParameters`/`kms:Decrypt` on exactly the ARNs in `secrets` and `secrets_kms_key_arns`. Task role: nothing, unless `extra_policy_jsons` grants it (Allow statements may not use `*`, `<service>:*`, non-read wildcards, `NotAction` or `NotResource`, may not allow `iam:PassRole`, and may not use `iam:` actions on `Resource "*"`; this is stricter than `modules/lambda` 1.0.0). Other explicit actions on `Resource "*"` (for example `kms:Decrypt`) are accepted, so review the documents; the permissions boundary is the backstop. |
| IAM guardrails | Both roles live under `/iacp/` and always carry a permissions boundary (`permissions_boundary_arn`, or the account's `iacp-boundary` policy). The trust policy only admits `ecs-tasks.amazonaws.com` for the owning account (`aws:SourceAccount`, `aws:SourceArn`). |
| Secrets | Injected by reference (`valueFrom`) from Secrets Manager or SSM; plain values are rejected by `secrets` validation. |
| Network | The tasks' security group admits only the container port from the load balancer's group (nothing at all without a load balancer). Egress: TCP 443 to `https_egress_cidr_blocks` for AWS APIs, plus the security groups listed in `egress_security_group_rules`. The load balancer admits 443 and 80 from `ingress_cidr_blocks` and may only reach the tasks. |
| Load balancer | Internal by default. HTTPS listener with the given ACM certificate and a TLS 1.2+ policy (`ELBSecurityPolicy-TLS13-1-2-2021-06` by default); port 80 only answers `301` to HTTPS. Invalid headers dropped, defensive desync mitigation, deletion protection in production. |
| Deployments | Circuit breaker with rollback, 100 % minimum healthy, health-check grace period behind a load balancer. |
| Observability | Container logs to `/ecs/<name>` with the module key and a retention (default 30 days). A module-created cluster enables Container Insights. |
| Tagging | `iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:module`, `iacp:managed-by` are merged last on every taggable resource and propagated from the service to its tasks. |

Load balancer access logs, WAF association, autoscaling and default alarms are
planned for later minor releases. TLS ends at the load balancer; traffic to
the tasks stays inside the VPC and is limited to the load balancer's security
group.

## Inputs

| Name | Description | Type | Default | Required |
|---|---|---|---|:---:|
| `name` | Logical name. Resources are named `<team>-<environment>-<name>` (at most 50 characters, no consecutive hyphens); the load balancer and target group use a hashed 32-character form when longer. | `string` | n/a | yes |
| `team` | Owning team slug, without trailing or consecutive hyphens. Injected by the platform. | `string` | n/a | yes |
| `environment` | `development`, `staging` or `production`. Injected by the platform. | `string` | n/a | yes |
| `stack` | Owning stack name, used for the `iacp:stack` tag. Injected by the platform. | `string` | n/a | yes |
| `tags` | Additional tags. Keys starting with `iacp:` are reserved. At most 30. | `map(string)` | `{}` | no |
| `vpc_id` | VPC that holds the service and its load balancer. | `string` | n/a | yes |
| `subnet_ids` | 1-16 private subnets for the tasks. | `list(string)` | n/a | yes |
| `https_egress_cidr_blocks` | IPv4 ranges the tasks may reach on 443. | `list(string)` | `["0.0.0.0/0"]` | no |
| `egress_security_group_rules` | Extra egress `{ description, port, security_group_id }` (at most 20). | `list(object)` | `[]` | no |
| `load_balancer` | `{ internal = optional(true), subnet_ids, certificate_arn, ingress_cidr_blocks, ssl_policy = optional, idle_timeout = optional(60) }`, or null for a worker. | `object` | `null` | no |
| `cluster_arn` | Existing ECS cluster ARN. Null creates one with Container Insights. | `string` | `null` | no |
| `cpu` | 256, 512, 1024, 2048 or 4096. | `number` | `256` | no |
| `memory` | MiB; a valid Fargate combination for `cpu`. | `number` | `512` | no |
| `architecture` | `X86_64` or `ARM64`. | `string` | `"X86_64"` | no |
| `desired_count` | Tasks to run (0-100). | `number` | `1` | no |
| `image_tag` | Tag in the service's repository; not `latest`, `main` or `master`. | `string` | n/a | yes |
| `container_port` | Container port (1024-65535). | `number` | `8080` | no |
| `container_user` | Non-root `UID[:GID]`. | `string` | `"1000:1000"` | no |
| `health_check_path` | Path probed for 200-399. | `string` | `"/health"` | no |
| `environment_variables` | Non-secret configuration (no `AWS_`/`ECS_` keys). | `map(string)` | `{}` | no |
| `secrets` | `NAME = <Secrets Manager or SSM parameter ARN>`. | `map(string)` | `{}` | no |
| `secrets_kms_key_arns` | KMS keys encrypting those secrets. | `list(string)` | `[]` | no |
| `permissions_boundary_arn` | Boundary for both roles. Null uses `arn:<partition>:iam::<account>:policy/iacp-boundary`. | `string` | `null` | no |
| `extra_policy_jsons` | Up to 10 task-role policy documents keyed by a stable name. Allow statements may not use `*`, `<service>:*`, non-read wildcards, `NotAction`, `NotResource`, `iam:PassRole`, or any `iam:` action on `Resource "*"`. | `map(string)` | `{}` | no |
| `ecr_max_image_count` | Tagged images kept (1-1000). | `number` | `30` | no |
| `log_retention_days` | CloudWatch Logs retention. | `number` | `30` | no |
| `kms_key_arn` | Existing customer-managed KMS key ARN for the repository and logs. If null, a dedicated key is created. Must be known at plan time. | `string` | `null` | no |

## Outputs

| Name | Description |
|---|---|
| `id` / `arn` | ECS service ARN. |
| `name` | Computed name (`<team>-<environment>-<name>`). |
| `service_name` | ECS service name. |
| `cluster_arn` | Cluster running the service. |
| `alb_dns_name` | Load balancer DNS name, or null. |
| `service_url` | `https://<alb_dns_name>`, or null. |
| `ecr_repository_url` | Repository CI pushes to. |
| `task_role_arn` / `execution_role_arn` | Role ARNs. |
| `security_group_id` | Security group of the tasks. |
| `log_group_name` | Container log group. |
| `kms_key_arn` | KMS key encrypting the repository and logs. |
| `iacp_metadata` | `{ module = "ecs-service", version = "1.0.0" }` for inventory and drift correlation. |

## Resources

`aws_ecs_service`, `aws_ecs_task_definition`, optional `aws_ecs_cluster`,
`aws_ecr_repository` + `aws_ecr_lifecycle_policy`, two `aws_iam_role`s with an
inline execution policy and one inline task policy per `extra_policy_jsons`
key, `aws_cloudwatch_log_group`, security groups with separate rule
resources, optional `aws_lb` + `aws_lb_target_group` + two `aws_lb_listener`s,
and optional `aws_kms_key` + `aws_kms_alias`.

## Testing

LocalStack community (the pinned 4.4 image) does not emulate ECS, ECR or
ELBv2, so this module is tested without an emulator:

- [`tests/ecs_service.tftest.hcl`](tests/ecs_service.tftest.hcl) runs `plan`
  assertions with the real AWS provider fully offline (dummy credentials, no
  account lookup, every endpoint pointed at a closed local port, the
  account-reading data sources replaced by `override_data`). One `apply` run
  uses `mock_provider "aws"` in its own state to assert the applied
  attributes: ECR scanning, immutability, KMS and lifecycle; the container's
  non-root, read-only settings; no public IP; the circuit breaker; ALB-only
  ingress; the HTTPS listener and the redirect; exact execution-role
  permissions; the `/iacp/` path and boundary; tags and outputs. Plan runs
  cover a public production load balancer, a worker without one, name
  shortening and custom boundaries; validation runs check that unsafe inputs
  (`latest`, root, privileged ports, legacy TLS policies, plain-text secrets,
  wildcard task policies) are rejected.
- [`examples/complete`](examples/complete) is planned, never applied, and
  checked with conftest against `policies/opa` (`policies/example-plan.sh`).

```bash
terraform -chdir=modules/ecs-service init -backend=false
terraform -chdir=modules/ecs-service test    # offline, no LocalStack needed
make up && policies/example-plan.sh modules/ecs-service
```

In CI the suite runs in the `module-tests-offline` job, and the example plan
in the `module-tests` job (its LocalStack container only answers STS).
