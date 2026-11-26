# tools/

Python developer tooling. Each tool is a small, self-contained package with tests. Run tools from the repo root with the project virtualenv:

```bash
.venv/bin/python -m tools.<tool> --help
```

| Tool | Purpose | Make target |
|------|---------|-------------|
| `manifest_validator` | Validates `modules/*/module.yaml` against the schema and checks parity with `variables.tf`/`outputs.tf`. | `make validate-manifests` |
| `module_release` | SemVer interface-diff gate, release notes and catalog indexing for `modules/<name>` (used by `.github/workflows/module-release.yml`). | `make module-gate` |

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
