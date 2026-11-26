# modules/s3 — complete example

Exercises every input of the module against LocalStack (ADR-0009): a bucket
with a lifecycle transition and access logging to a second bucket that grants
`logging.s3.amazonaws.com`. Terratest (`tests/s3_test.go`) applies it.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform apply && terraform destroy
```

## Policy check

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json
```

Expected result: no denies except `iacp.forbidden.module_source` for each
module call. The example calls the module through a relative path (`../../`)
so it tests the working tree. Generated stacks always use a pinned
`git::…?ref=modules/s3/vX.Y.Z` source, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
