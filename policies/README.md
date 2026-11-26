# policies/

Policy as code that runs on every change.

- `opa/`: Rego policies on Terraform plan JSON, run with `conftest test plan.json -p policies/opa` (the `main` package aggregates every `iacp.*` package), unit tested with `opa test policies/opa --coverage` (CI gate: 90 %). Optional stack context is passed with `--data context.json` as `data.iacp.context`.
- `fixtures/`: a compliant and a non-compliant plan JSON plus a stack `context.json`. `fixtures/verify.sh` checks that the first produces no denies or warnings and the second trips every deny and warn rule ID; It then reruns the compliant plan with `context-yanked.json`, which lists its pinned `modules/s3/v1.2.0` as yanked, and expects `iacp.modules.yanked` alone. `fixtures/regenerate.sh` rebuilds the plans offline from `fixtures/src/`. They live outside `opa/` because `opa test` loads every JSON file under its path as data.
- `example-plan.sh modules/<name>`: plans the module's `examples/complete` (never applied; LocalStack only answers STS while planning) and runs conftest with the example's `policy-context.json`. The only deny it accepts is `iacp.forbidden.module_source` for a source starting with `../` (examples use relative sources); `?ref=main`, third-party or other unpinned sources fail. `--plan plan.json --context ctx.json` gates an existing plan JSON, and `fixtures/verify-example-gate.sh` (run by `make policy-test`) proves the gate with three fixture plans in `fixtures/example-gate/`. `make example-plans` runs it for every module; CI runs it in the `module-tests` job.
- `checkov/`: custom Checkov checks (`IACP_AWS_*`) that run alongside the built-in ones with `checkov --external-checks-dir policies/checkov`. Tests: `python -m pytest policies/checkov` (deps in `checkov/requirements.txt`).

See [docs/07-security-and-policy.md](../docs/07-security-and-policy.md).
