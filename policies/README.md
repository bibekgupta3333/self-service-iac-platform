# policies/

Policy as code that runs on every change.

- `opa/`: Rego policies on Terraform plan JSON, run with `conftest test plan.json -p policies/opa` (the `main` package aggregates every `iacp.*` package), unit tested with `opa test policies/opa --coverage` (CI gate: 90 %). Optional stack context is passed with `--data context.json` as `data.iacp.context`.
- `fixtures/`: a compliant and a non-compliant plan JSON plus a stack `context.json`. `fixtures/verify.sh` checks that the first produces no denies or warnings and the second trips every deny and warn rule ID; It then reruns the compliant plan with `context-yanked.json`, which lists its pinned `modules/s3/v1.2.0` as yanked, and expects `iacp.modules.yanked` alone. `fixtures/regenerate.sh` rebuilds the plans offline from `fixtures/src/`. They live outside `opa/` because `opa test` loads every JSON file under its path as data.
- `checkov/`: custom Checkov checks (`IACP_AWS_*`) that run alongside the built-in ones with `checkov --external-checks-dir policies/checkov`. Tests: `python -m pytest policies/checkov` (deps in `checkov/requirements.txt`).

See [docs/07-security-and-policy.md](../docs/07-security-and-policy.md).
