# modules/ecs-service — complete example

An HTTPS API on Fargate behind an internal load balancer, with its PostgreSQL
database, wired the way the platform wires stacks:

- the postgres module admits only the service's security group;
- the service may reach only the database's security group (plus HTTPS to AWS
  APIs);
- the RDS-managed master secret is injected by reference, and the execution
  role may read only that secret and decrypt only with the database's key.

A stand-in VPC and two private subnets take the place of the vpc stack's
outputs, and the certificate ARN is a placeholder.

This example is **plan-only**. LocalStack community (ADR-0009) does not
emulate ECS, ECR, ELBv2 or RDS, and planning new resources makes no AWS call
except STS (for the account ID), which LocalStack answers.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform plan
```

## Policy check

```bash
policies/example-plan.sh modules/ecs-service     # from the repository root
```

or by hand:

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json
```

Expected result: no denies except `iacp.forbidden.module_source` for the two
module calls. The example calls modules through relative paths so it tests
the working tree. Generated stacks always use pinned
`git::…?ref=modules/<name>/vX.Y.Z` sources, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
