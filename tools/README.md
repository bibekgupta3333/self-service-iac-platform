# tools/

Python developer tooling. Each tool is a small, self-contained package with tests. Run tools from the repo root with the project virtualenv:

```bash
.venv/bin/python -m tools.<tool> --help
```

| Tool | Purpose | Make target |
|------|---------|-------------|
| `manifest_validator` | Validates `modules/*/module.yaml` against the schema and checks parity with `variables.tf`/`outputs.tf`, and `compatibility` with `required_version`/`required_providers`. | `make validate-manifests` |
| `module_release` | SemVer interface-diff gate, release notes and catalog indexing for `modules/<name>` (used by `.github/workflows/module-release.yml`). | `make module-gate` |
| `codeowners` | Generates `.github/CODEOWNERS` from `.github/teams.yaml`: default and platform owners, then `/environments/<env>/<team>/` for every declared team (`@<org>/<team>` unless owners are listed, with per-environment overrides). `--check` fails on drift or on a git-tracked stack whose team is not declared (CI). `platform_paths` under `/environments/<env>/` are rejected, since team rules would always override them. Branch ruleset and Environment payloads: `.github/rulesets/`. | `make codeowners`, `make codeowners-check` |
| `stack_pipeline` | CI helpers for stack roots under `environments/` (used by `.github/workflows/pr-validate.yml` and `apply.yml`): `detect` (changed stacks as a JSON matrix, fail-closed), `context` (conftest `--data`), `summary` (sticky PR comment and JSON payload), `verify-pins` (published tag-to-commit check before init; serves unpublished tags from a local mirror only with `--allow-unpublished`), `verify-modules` (installed modules match the pins one to one after init), `report` (one stage report to `POST /v1/runs`, used by `.github/actions/iacp-report`), `lock acquire` and `lock release` (platform stack lock for `apply.yml`), `summary --cost` and `fold-cost` (add an Infracost estimate to the summary, informational), `cost-input` (the plan for the cost job, sensitive values nulled), `sarif` (stack TFLint/Checkov SARIF to repository paths for code scanning), `smoke` (post-apply: declared outputs and `iacp:*` tags in state and read back with the AWS CLI). Pure logic; git, filesystem, AWS CLI and HTTP access (`api_client.py`: OIDC or internal token, redirects refused) in adapters. | `make stack-detect`, `make stack-plan` |
| `provider_floor` | `stage <module> <dest>`: copies a module and adds an override file that pins each provider to the floor of its `required_providers` constraint, so CI can run the offline `terraform test` suites on the oldest allowed provider (`module-tests-floor` job in `ci.yml`). | |
| `required_checks` | `--check`: fails when a required status check in `.github/rulesets/main-branch.json` is not the `name:` of a workflow job. | `make required-checks-check` |

## module_release

```bash
python -m tools.module_release gate s3              # or: gate --all [--format json]
python -m tools.module_release detect --base origin/main --include-untagged [--released-tags FILE]
python -m tools.module_release info s3              # version=, tag=, prerelease=, tag_exists=
python -m tools.module_release notes s3             # CHANGELOG section or a generated diff summary
python -m tools.module_release index s3 --commit-sha <sha> [--dry-run] [--skip-if-unconfigured]
```

The gate compares the module's variables (type, default or required, `nullable`, validation conditions), outputs (name, `sensitive`) and the floors of `required_version` and `required_providers` between the last final `modules/<name>/vX.Y.Z` tag and the working tree. It also fails when the declared version is already tagged and the module changed since. The rules are in [docs/04 section 5.2](../docs/04-module-registry.md#52-semver-rules-for-infrastructure-modules). It needs the tags locally (`git fetch --tags`). Exit codes: 0 passed, 1 gate failed, 2 usage or I/O error.

`index` posts to `$IACP_API_URL/v1/internal/catalog/index` with `Authorization: Bearer $IACP_INTERNAL_TOKEN`. Plain `http` is only accepted for loopback hosts, and redirects are refused. `--skip-if-unconfigured` exits 0 only when both variables are unset. To try it against a local API (`make up && make api`):

```bash
IACP_API_URL=http://127.0.0.1:8080 IACP_INTERNAL_TOKEN=local-dev-internal-token-0123456789abcdef \
  .venv/bin/python -m tools.module_release index s3 --commit-sha "$(git rev-parse HEAD)"
IACP_INTEGRATION=1 IACP_API_URL=http://127.0.0.1:8080 IACP_INTERNAL_TOKEN=local-dev-internal-token-0123456789abcdef \
  .venv/bin/python -m pytest -q tools/module_release   # includes the live index test
```
