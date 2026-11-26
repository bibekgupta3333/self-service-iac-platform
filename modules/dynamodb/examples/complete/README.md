# modules/dynamodb — complete example

Exercises the module against LocalStack (ADR-0009): a composite key, a global
secondary index with an `INCLUDE` projection, TTL on `expires_at` and a
`NEW_AND_OLD_IMAGES` stream. Encryption with a module-created key and
point-in-time recovery are secure defaults. Deletion protection stays off
because the example runs in `development`. Terratest (`tests/dynamodb_test.go`)
applies it.

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
`git::…?ref=modules/dynamodb/vX.Y.Z` source, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
