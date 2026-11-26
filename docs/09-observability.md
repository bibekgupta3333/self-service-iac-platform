# 09 — Platform observability

> Purpose: describe the platform's own CloudWatch dashboard and alarms (WBS 7.7): what each panel and alarm means, why the thresholds are what they are, what to do when one fires, and which signals the backend does not emit yet.

Related: [02-aws-infrastructure.md §8](02-aws-infrastructure.md#8-observability) · [06-lifecycle-management.md](06-lifecycle-management.md) · [08-mvp-roadmap.md](08-mvp-roadmap.md) · [ADR-0009](adr/0009-local-first-localstack-and-local-backend.md)

## Contents

1. [What is deployed](#1-what-is-deployed)
2. [From Prometheus to CloudWatch](#2-from-prometheus-to-cloudwatch)
3. [Dashboard panels](#3-dashboard-panels)
4. [Alarms](#4-alarms)
5. [Gaps](#5-gaps)
6. [Running it locally](#6-running-it-locally)

## 1. What is deployed

The stack `environments/development/platform/observability/` is a generated stack root like every other (do not hand-edit it; see AGENTS.md rule 7). It holds one module instance, `modules/monitoring` pinned to `v1.0.0`, and creates:

| Resource | Name | Notes |
|----------|------|-------|
| SNS topic (KMS-encrypted, key with rotation) | `platform-development-observability` | Target of every alarm action and OK action. No subscription is configured yet, so alarms are visible in CloudWatch but notify nobody. Add an email or https subscription to `subscriptions` in the stack's inputs. |
| CloudWatch dashboard | `platform-development-observability` | Nine widgets plus the alarm status widget the module appends. |
| CloudWatch alarms | `platform-development-observability-sqs-<key>-*` for the keys `events`, `events-dlq`, `github-outbound` | Six alarms on the event queue, its DLQ and the GitHub outbound queue. |

Other environments get their own copy of the stack (`environments/<env>/platform/observability/`) with the `Environment` dimension changed; only `development` exists today.

## 2. From Prometheus to CloudWatch

The API serves Prometheus text on `/metrics` (`backend/internal/observability`). **No exporter publishes to CloudWatch yet**, so every panel that reads the `IACP/Platform` namespace is empty until one does. Panels on `AWS/SQS` read metrics AWS publishes itself.

### Naming rule the exporter must follow

| Prometheus | CloudWatch |
|------------|------------|
| Namespace | `IACP/Platform` (docs/02 used the placeholder `the platform`; this is the real name) |
| Metric name | `iacp_` prefix and `_total` suffix dropped, rest in UpperCamelCase: `iacp_http_requests_total` becomes `HttpRequests` |
| Counter | published as the per-period delta, read with `Sum` |
| Gauge | published as is, read with `Maximum` (or `Average`) |
| Histogram | published as a statistic set so `p50`, `p95` and `Maximum` work |
| Labels | dropped unless listed below; `Environment` is always added. `route` and `method` are never dimensions (cardinality and cost) |

### What exists in the backend today

| Prometheus metric | Labels | CloudWatch metric | Dimensions | Panel |
|-------------------|--------|-------------------|------------|-------|
| `iacp_http_requests_total` | `method`, `route`, `status` | `HttpRequests` | `Environment`, `StatusClass` (`status` folded to `2xx`..`5xx`) | API from /metrics |
| `iacp_http_request_duration_seconds` | `method`, `route` | `HttpRequestDurationSeconds` | `Environment` | API from /metrics |
| `iacp_db_pool_connections` | `state` | `DbPoolConnections` | `Environment`, `State` | API from /metrics |
| `iacp_db_pool_max_connections` | none | not mapped | | |
| Go runtime and process collectors | | not mapped | | |

### What the dashboard needs and the backend does not emit

These are the gaps. The data exists in PostgreSQL; nothing computes or exposes it as a metric. Each needs a backend change in a separate PR (this change did not touch `backend/`).

| CloudWatch metric | Dimensions | Source of truth | Panel |
|-------------------|------------|-----------------|-------|
| `RequestLeadTimeSeconds` | `Environment` | `requests.applied_at - created_at` on `applied` | Request lead time |
| `RequestTimeToPlanSeconds` | `Environment` | plan posted time from `pipeline_runs` minus `requests.created_at` (NFR-01) | Request lead time |
| `RequestsFinished` | `Environment`, `Outcome` (`applied`, `failed`) | request transitions to a terminal status | Requests finished |
| `RequestFailureRatePercent` | `Environment` | `failed / (applied + failed)` over the publish period, published pre-computed because module widgets cannot do metric math | Request failure rate |
| `ApplyDurationSeconds` | `Environment` | `pipeline_runs` apply stage start and finish | Apply duration |
| `LockQueueDepth` | `Environment` | count of requests with `queued_behind` set | Lock queue depth |
| `LockOldestHolderAgeSeconds` | `Environment` | `now() - min(locks.acquired_at)` | Lock queue depth |
| `Stacks` | `Environment`, `Status` | `stacks` grouped by `status` | Stacks by status |
| `OutboxPending`, `OutboxOldestPendingAgeSeconds`, `OutboxDead` (not on the dashboard yet) | `Environment` | `outbox` rows pending, oldest `created_at`, `dead_at IS NOT NULL` | none; see [§5](#5-gaps) |

Until the exporter exists the same numbers can be read from Postgres with the queries implied by the source column.

## 3. Dashboard panels

Thresholds below come from the requirements (docs/00 NFR-01 and NFR-02, docs/08 MVP exit criteria) and docs/02 §8. The lines on the panels are not drawn; compare by eye.

| Panel | Series | Healthy | What it means | What to do when it is off |
|-------|--------|---------|---------------|---------------------------|
| **Request lead time** | `RequestLeadTimeSeconds` p50 and p95, `RequestTimeToPlanSeconds` p95 | plan p95 under 10 min, applied p50 under 15 min in development, excluding human review wait | How long a developer waits. Plan time is mostly runner start and `terraform plan`; applied time adds review and apply. | Check the pr-validate run queue and runner availability first, then `plan` stage duration in `pipeline_runs`. A rise in applied time with a flat plan time means review wait or lock queueing: look at **Lock queue depth**. |
| **Request failure rate (%)** | `RequestFailureRatePercent` hourly average | under 20 % per hour (the docs/02 pipeline failure threshold), ideally near 0 in development | Share of finished requests that ended `failed`, not `rejected` or `cancelled`. | Open the failed requests (`GET /v1/requests?status=failed`), group by failed stage. A single module or provider error is a module bug: yank or patch the module version. Many different stages failing at once is platform trouble (GitHub, LocalStack, state, credentials). |
| **Requests finished** | `RequestsFinished` applied vs failed, stacked | failed bars rare | Volume context for the rate: three failures out of four requests reads very differently from three out of three hundred. | Use alongside the failure rate before paging anyone. |
| **Apply duration** | `ApplyDurationSeconds` p50, p95, max | p95 stable per module mix | Time inside the `apply` job. A creeping p95 means a slower module, API throttling, or retries. | Compare against the module cost and the request mix. A max near the job timeout means a hung apply: see the runner-died runbook in [06 §3](06-lifecycle-management.md#3-state-locking). |
| **Lock queue depth** | `LockQueueDepth` max, `LockOldestHolderAgeSeconds` max | depth 0 to 2, oldest holder under a few hours | Requests waiting behind a stack lock, and how long the longest lock has been held. Locks last from request creation to a terminal status, so a long hold usually means a PR nobody has reviewed. | Find the holder (`locks` row or the stack in the portal), ping its owner, or cancel the request. At day 5 the worker warns the requester and at day 7 the lease expires. If the holder is `apply_interrupted`, follow the stale-lock runbook in [06 §3](06-lifecycle-management.md#3-state-locking). |
| **Stacks by status** | `Stacks` per status: active, drifted, failed, updating | `failed` and `drifted` at 0 | Fleet health from `stacks.status`. `failed` stacks only accept remediation requests; `drifted` stacks have unreviewed changes in AWS. | `failed`: retry apply or roll back per [06 §5](06-lifecycle-management.md). `drifted`: read the drift report and reconcile or revert. `updating` stuck for hours points at a stuck lock or apply. |
| **Event and GitHub outbound queue backlog** | `iacp-events` and `iacp-github-outbound` visible, `iacp-events-dlq` visible, oldest message age of both (AWS/SQS) | visible near 0, age under 300 s, DLQ at 0 | The outbox-to-SQS path (docs/01 and docs/06: every transition enqueues follow-up work). A backlog means the worker is not draining it. | See the matching alarms in §4. |
| **API from /metrics** | `HttpRequests` 5xx, `HttpRequestDurationSeconds` p95, `DbPoolConnections` acquired | 5xx near 0, p95 under 1 s (docs/02), acquired well below the pool max | API errors, latency and connection saturation, the only panel backed by metrics that exist in the backend today. | 5xx: read the structured logs by `request_id`. Latency with high `acquired`: the pool is exhausted, look for slow queries before raising the pool size. |
| **Alarms** (appended by the module) | status of all six alarms | all green | Quick view of what is firing. | Follow §4. |

## 4. Alarms

All six alarms evaluate one 5-minute period, treat missing data as not breaching (so a quiet queue stays green), and publish to the topic both when they fire and when they recover.

| Alarm | Metric and threshold | Why this value | When it fires |
|-------|----------------------|----------------|---------------|
| `…-sqs-events-oldest-message-age` | `ApproximateAgeOfOldestMessage` on `iacp-events` greater than 300 s | docs/02 §8 sets 300 s (P2). Follow-up work (PR publish, webhook commands) should be consumed within seconds, so five minutes means the consumer is stalled, not slow. | The worker is down, wedged on a poison message, or cannot reach GitHub or Postgres. Check worker health and logs (`/iacp/worker`), the GitHub API rate limit, then Postgres. Requests will sit in `draft` or `pr_open` without progressing. |
| `…-sqs-events-visible-messages` | `ApproximateNumberOfMessagesVisible` on `iacp-events` greater than 100 | The platform serves tens of teams; a healthy queue holds a handful of messages. 100 is a burst or a stall that the age alarm has not caught yet (many fast messages, none old). | A webhook storm (mass push or re-delivery) or slowed consumers. Confirm the worker scaled, check for a redelivery loop, and only then consider scaling. |
| `…-sqs-events-dlq-visible-messages` | `ApproximateNumberOfMessagesVisible` on `iacp-events-dlq` greater than 0 | Any dead-lettered message is a request that will not progress by itself (docs/02 §8, P2). | A message failed its retries. Read it (`aws sqs receive-message` on the DLQ), find the request, fix the cause (bad payload, missing permission, GitHub outage), then redrive. Do not purge before the request is accounted for. |
| `…-sqs-github-outbound-oldest-message-age` | `ApproximateAgeOfOldestMessage` on `iacp-github-outbound` greater than 300 s | The queue holds retries of GitHub writes (docs/02 §3: PR creation, labels, dispatch). Retries back off, but five minutes with a message still waiting means GitHub is rejecting or unreachable. | Check GitHub status, the App installation token and the rate limit. Requests stay in `draft` until their PR is published. |
| `…-sqs-github-outbound-visible-messages` | `ApproximateNumberOfMessagesVisible` on `iacp-github-outbound` greater than 100 | Same burst reasoning as the event queue. | Look for a loop re-enqueueing the same write, then wait out the GitHub incident; do not purge. |
| `…-sqs-events-dlq-oldest-message-age` | `ApproximateAgeOfOldestMessage` on `iacp-events-dlq` greater than 60 s | Re-notifies while a dead letter sits unhandled; with the 5-minute period it keeps the alarm in the failing state until the queue is empty. | Same as above; it clears only when the DLQ is drained. |

The queue names are inputs, not dependencies: the stack does not create the queues. They belong to the platform account infrastructure ([02 §3](02-aws-infrastructure.md)), so the alarms exist and stay `OK` (no data) in development, where nothing publishes to SQS. In the local-first phase the same job is done by the Postgres `outbox` table (see [§5](#5-gaps)).

### Alarms specified but not deployed

`modules/monitoring` v1.0.0 creates alarms only for SQS queues, Lambda functions and DynamoDB tables. It has no input for an alarm on a custom metric, so these two cannot be expressed as module inputs, and a raw alarm resource must not be hand-added to a generated stack root. They are blocked on a `monitoring` minor release that adds the input (see [§5](#5-gaps)). Their intended definitions:

| Alarm | Metric and threshold | Why | What to do when it fires |
|-------|----------------------|-----|--------------------------|
| Request failure rate | `RequestFailureRatePercent` above 20 for one hour (average) | docs/02 §8: non-policy pipeline failure rate above 20 % per hour (P2). Policy denials are the user's to fix and are excluded when the exporter computes the rate. | Same as the failure-rate panel: group failures by stage, then module version. |
| Stuck lock | `LockOldestHolderAgeSeconds` above 86400 (24 h) | A lock outlives its request only when something is wrong: a PR waiting for review for a whole working day, or an interrupted apply. The lease is 7 days with a warning on day 5; 24 h catches it while the owner can still act and long before the lease expires. Revisit the value after real traffic. | Find the holder and its state. Waiting on review: nudge the reviewer or cancel. `apply_interrupted`: run the stale-lock runbook ([06 §3](06-lifecycle-management.md#3-state-locking)). The lock is released by the worker, never by editing the `locks` row. |

## 5. Gaps

| # | Gap | Effect | Where it is fixed |
|---|-----|--------|-------------------|
| G1 | No CloudWatch exporter for the Prometheus registry (or EMF publisher, as docs/02 §8 intends) | All `IACP/Platform` panels are empty | Backend: a publisher adapter that applies the naming rule in §2 |
| G2 | The backend exposes only HTTP and DB pool metrics; none of the request, apply, lock or stack metrics in §2 exist | Lead time, failure rate, apply duration, lock and stack panels have no data source | Backend: a metrics collector reading `requests`, `pipeline_runs`, `locks` and `stacks` (blocked on the runs, TTL and drift work for the apply and lock series) |
| G3 | `modules/monitoring` has no custom-metric alarm input | The failure-rate and stuck-lock alarms cannot be deployed | A `monitoring` v1.1.0 adding a `metric_alarms` map (namespace, metric name, dimensions, statistic, period, threshold, comparison), released through the module release workflow, then a stack update pinned to it |
| G4 | The local-first platform has no SQS: the outbox is the Postgres table (`outbox`, migration 0008) and the `iacp-events` queues do not exist | The queue alarms cannot fire in development; the outbox backlog has no CloudWatch signal | Backend: publish `OutboxPending`, `OutboxOldestPendingAgeSeconds` and `OutboxDead`, and add an outbox panel and alarms once G1 and G3 are done |
| G5 | The topic has no subscriptions | Alarms do not notify anyone | Add `subscriptions` through a platform update request for this stack (a real email address or https endpoint); the generator then renders it. Do not edit the generated `main.tf` |
| G6 | docs/02 §8 lists other dashboards and alarms (Pipeline, Lifecycle, Cost, Service) | Out of scope for the MVP observability stack | Later, as the metrics in G2 appear |

## 6. Running it locally

The stack follows the same gate as every stack (`pr-validate.yml`), and runs unchanged against LocalStack (CloudWatch, SNS, KMS and SQS are in the `SERVICES` list of `docker-compose.yml`):

```bash
make up
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1
STACK=environments/development/platform/observability
make stack-plan STACK=$STACK
make stack-apply STACK=$STACK
aws --endpoint-url http://localhost:4566 cloudwatch list-dashboards
aws --endpoint-url http://localhost:4566 cloudwatch describe-alarms
make stack-destroy STACK=$STACK
make stack-clean STACK=$STACK
```

LocalStack 4.4 drops the tags passed to `PutMetricAlarm`. Two consequences, neither a stack defect:

- a second `terraform plan` after the apply proposes an in-place tag update on the six alarms; the pr-validate round trip accepts exactly that difference (`tools.stack_pipeline converged`) and nothing else;
- the post-apply smoke test (`tools.stack_pipeline smoke`) does not check tags on `aws_cloudwatch_metric_alarm`, and its report names the type as not checked. Tags on the topic and the key are still verified, in state and on the live resource.

Real AWS keeps the tags, but nothing switches the exceptions off by itself: `EMULATOR_UNTAGGED` in `tools/stack_pipeline/smoke.py` applies to every run, and the exception also lives in `convergence.py`. A test ties the set to the pinned LocalStack image (4.4); when that pin moves, re-test `PutMetricAlarm` tagging and empty the set by hand if LocalStack keeps the tags.
