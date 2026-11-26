# modules/monitoring — complete example

Exercises the module against LocalStack (ADR-0009), wired the way the
platform wires stacks: the sqs module's own alarms notify the monitoring
topic, and the monitoring module adds age and depth alarms for the same
queue, Lambda error and throttle alarms, a dashboard with a text panel and a
metric graph, and an email subscription (pending until confirmed). Terratest
(`tests/monitoring_test.go`) applies it.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform apply && terraform destroy
```

## Policy check

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json

# The same example as a staging stack, where the stricter branches apply
# (customer-managed keys required).
terraform plan -var environment=staging -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.staging.json
```

Expected result for both contexts: no denies except `iacp.forbidden.module_source` for each of
the two module calls. The example calls modules through relative paths so it
tests the working tree. Generated stacks always use pinned
`git::…?ref=modules/<name>/vX.Y.Z` sources, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
