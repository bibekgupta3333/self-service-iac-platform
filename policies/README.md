# policies/

Policy as code that runs on every change.

- `opa/`: Rego policies on Terraform plan JSON, run with `conftest test plan.json -p policies/opa`, unit tested with `opa test policies/opa`.
- `checkov/`: custom Checkov checks (`IACP_*`) that run alongside the built-in ones.

See [docs/07-security-and-policy.md](../docs/07-security-and-policy.md).
