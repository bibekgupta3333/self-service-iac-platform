# policies/

Policy as code that runs on every change.

- `opa/`: Rego policies on Terraform plan JSON, run with `conftest test plan.json -p policies/opa` (the `main` package aggregates every `iacp.*` package), unit tested with `opa test policies/opa --coverage` (CI gate: 90 %). Optional stack context is passed with `--data context.json` as `data.iacp.context`.
- `fixtures/`: a compliant and a non-compliant plan JSON. `fixtures/verify.sh` checks that the first passes and the second trips every rule; `fixtures/regenerate.sh` rebuilds them offline from `fixtures/src/`. They live outside `opa/` because `opa test` loads every JSON file under its path as data.
- `checkov/`: custom Checkov checks (`IACP_AWS_*`) that run alongside the built-in ones with `checkov --external-checks-dir policies/checkov`. Tests: `python -m pytest policies/checkov` (deps in `checkov/requirements.txt`).

See [docs/07-security-and-policy.md](../docs/07-security-and-policy.md).
