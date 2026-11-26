# modules/sqs — complete example

Exercises the module against LocalStack (ADR-0009): custom timings, redrive
count, `key_service_principals`, and alarm notifications to an SNS topic
encrypted with a customer managed key that CloudWatch may use. Terratest
(`tests/sqs_test.go`) applies it.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform apply && terraform destroy
```

## Policy check

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json
```

Expected result: no denies except `iacp.forbidden.module_source` for the
module call. The example calls the module through a relative path (`../../`)
so it tests the working tree. Generated stacks always use a pinned
`git::…?ref=modules/sqs/vX.Y.Z` source, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
