# modules/lambda — complete example

A queue-driven function wired the way the platform wires stacks, on LocalStack
(ADR-0009). The s3 module stores the package (an inline Python handler zipped
with `archive_file`). The sqs module provides the trigger and its key. The
artifact bucket's `read_policy_json` is attached through `extra_policy_jsons`.

```bash
docker compose up -d localstack
terraform init -backend=false && terraform apply && terraform destroy
```

## Policy check

```bash
terraform plan -out tfplan && terraform show -json tfplan > plan.json
conftest test plan.json -p ../../../../policies/opa --data policy-context.json
```

Expected result: no denies except `iacp.forbidden.module_source` for each of
the three module calls. The example calls modules through relative paths so
it tests the working tree. Generated stacks always use pinned
`git::…?ref=modules/<name>/vX.Y.Z` sources, and that rule stays non-waivable.

There is also one warning, `iacp.iam.policy_unknown`, on
`aws_iam_role_policy.extra["artifacts-read"]`. The bucket in this example is
created in the same plan, so its read policy is only known after apply. In
platform stacks, the policy arrives as another stack's output and is checked
at plan time.
`tfplan`, `plan.json` and `handler.zip` are git-ignored.
