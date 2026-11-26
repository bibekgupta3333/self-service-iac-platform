# tools/

Python developer tooling. Each package follows the same 70/30 split: pure functions and value types for the logic, with git and filesystem access kept in adapters.

| Package | Purpose |
|---------|---------|
| `manifest_validator` | Validates `modules/*/module.yaml` against the schema and the module's `variables.tf` / `outputs.tf` |
| `stack_pipeline` | CI helpers for stack roots under `environments/`, used by `.github/workflows/pr-validate.yml`: `detect` (changed stacks as a JSON matrix), `context` (conftest `--data`), `summary` (sticky PR comment and JSON payload), `verify-pins` (module tag-to-commit checks; serves unpublished tags from a local mirror) |
 Each tool is a small, self-contained package with tests. Run tools from the repo root with the project virtualenv:

```bash
.venv/bin/python -m tools.<tool> --help
```
