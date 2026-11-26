# modules/postgres — complete example

A private PostgreSQL instance wired the way the platform wires stacks. The
stand-in VPC, two private subnets and an application security group take the
place of the vpc and ecs-service stacks' outputs.

This example is **plan-only**. LocalStack community (ADR-0009) does not
emulate RDS, and planning new resources makes no AWS call except STS (for the
account ID), which LocalStack answers.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform plan
```

## Policy check

```bash
policies/example-plan.sh modules/postgres     # from the repository root
```

or by hand:

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json
```

Expected result: no denies except `iacp.forbidden.module_source` for the
module call. The example calls the module through a relative path so it tests
the working tree. Generated stacks always use pinned
`git::…?ref=modules/<name>/vX.Y.Z` sources, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
