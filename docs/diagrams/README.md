# Diagrams

All diagrams in the platform design docs are generated from code. The Python
sources live in [`src/`](src/) and the rendered PNGs in [`images/`](images/).
Docs reference them relatively, e.g. `![Architecture](diagrams/images/architecture.png)`
from a file in `docs/`.

## Regenerating

Prerequisites:

- Python 3.10+ with the packages in [`requirements.txt`](requirements.txt)
  (`diagrams`, `matplotlib`)
- Graphviz (`dot` on `PATH`): `brew install graphviz` or `apt-get install graphviz`

From the repository root:

```bash
python3 -m venv .venv
.venv/bin/pip install -r docs/diagrams/requirements.txt
.venv/bin/python docs/diagrams/src/render_all.py
```

`render_all.py` runs every script below and writes the PNGs to `docs/diagrams/images/`.
Each script can also be run on its own, e.g. `.venv/bin/python docs/diagrams/src/seq_drift_detection.py`.

## Tooling

| Kind | How it is drawn |
| --- | --- |
| Architecture, infrastructure, pipeline, components | [mingrammer `diagrams`](https://diagrams.mingrammer.com/) (Graphviz under the hood); shared styling in `src/common.py` |
| ERD and state machines | Hand-written Graphviz DOT generated from Python (`src/dotutil.py`) |
| Sequence diagrams | `src/seqlib.py`, a small matplotlib renderer: participants and lifelines, numbered sync messages, dashed returns, self-messages, `alt` / `opt` / `loop` frames with `else` branches, and notes. Failure paths are drawn in red. |

## Diagram index

| Image | Source | Description |
| --- | --- | --- |
| [architecture.png](images/architecture.png) | `src/architecture.py` | Logical system architecture: developers, portal, Go API and worker, PostgreSQL, SQS, EventBridge Scheduler, GitHub App/repo/Actions, state bucket and workload accounts. |
| [aws-infrastructure.png](images/aws-infrastructure.png) | `src/aws_infrastructure.py` | AWS accounts and networking: platform account VPC (ALB + WAF, ECS Fargate, RDS Multi-AZ, SQS, Scheduler, Secrets Manager, KMS, state bucket, CloudWatch), dev/staging/prod accounts with `iacp-plan` / `iacp-apply` roles, GitHub OIDC trust, CloudTrail to the log archive. |
| [cicd-pipeline.png](images/cicd-pipeline.png) | `src/cicd_pipeline.py` | `pr-validate.yml` stages (fmt, validate, TFLint, Checkov, plan, OPA, Infracost, report), merge gates, and `apply.yml` stages (lock, re-plan + hash check, apply, smoke tests, outputs). |
| [component-backend.png](images/component-backend.png) | `src/component_backend.py` | Internal components of the Go backend: HTTP edge (middleware, auth, OIDC verifier, webhook ingester), domain services, generator, GitHub App client, lock manager, audit writer, store, and worker handlers. |
| [data-model.png](images/data-model.png) | `src/data_model.py` | PostgreSQL ERD with key columns and relationships, grouped into identity, catalog, stacks, requests/pipelines and audit. |
| [state-machine-request.png](images/state-machine-request.png) | `src/state_machine_request.py` | Request status (`draft` to `applied` / `failed` / `rejected` / `cancelled`) and stack status (`provisioning` to `destroyed` / `failed`) state machines. |
| [seq-provision-request.png](images/seq-provision-request.png) | `src/seq_provision_request.py` | Portal request to generated pull request, webhook, `pr-validate.yml` stages, callbacks and portal status. |
| [seq-apply-on-merge.png](images/seq-apply-on-merge.png) | `src/seq_apply_on_merge.py` | Merge triggers `apply.yml`: stack lock, OIDC role assumption, plan-hash verification, apply, outputs and audit; failure branch with retry and rollback. |
| [seq-drift-detection.png](images/seq-drift-detection.png) | `src/seq_drift_detection.py` | Nightly EventBridge Scheduler tick, `drift.yml` matrix running `plan -detailed-exitcode`, drift reports and owner notification. |
| [seq-ttl-teardown.png](images/seq-ttl-teardown.png) | `src/seq_ttl_teardown.py` | TTL reaper: 24h warning, destroy request PR, destroy apply, stack marked destroyed and audited. |
| [seq-module-release.png](images/seq-module-release.png) | `src/seq_module_release.py` | Module change PR, Terratest/LocalStack tests, merge, SemVer tag, manifest indexed into the catalog, portal shows the new version and upgrade path. |

## Conventions

- Colours: blue = the platform components, purple = GitHub, amber = AWS, green = data
  stores / success, red = failure paths.
- Keep names identical to the design docs (workflow file names, role names,
  table names, status values). Change the source, re-render, and commit both
  the script and the PNG.
