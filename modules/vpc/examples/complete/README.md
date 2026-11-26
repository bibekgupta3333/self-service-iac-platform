# modules/vpc — complete example

Exercises the module against LocalStack (ADR-0009): a custom CIDR, three
zones, NAT turned on explicitly with one gateway per zone (development leaves
NAT off by default), and a flow log that records rejected traffic for 14
days. Terratest (`tests/vpc_test.go`) applies it.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform apply && terraform destroy
```

## Policy check

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json

# The same example as a staging stack, where the stricter branches apply
# (customer-managed keys required, NAT on).
terraform plan -var environment=staging -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.staging.json
```

`make example-plans` (and CI's `module-tests` job) runs both checks through
`policies/example-plan.sh`. Expected result for both contexts: no denies except `iacp.forbidden.module_source` for the
module call. The example calls the module through a relative path (`../../`)
so it tests the working tree. Generated stacks always use a pinned
`git::…?ref=modules/vpc/vX.Y.Z` source, and that rule stays non-waivable.
`tfplan` and `plan.json` are git-ignored.
