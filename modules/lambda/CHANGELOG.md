# Changelog — modules/lambda

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/lambda/vX.Y.Z`.

## [1.0.0] - 2026-11-25

### Added
- Lambda function named `<team>-<environment>-<name>`, deployed from an
  `s3://` package built by the application's CI.
- Execution role under `/iacp/` with an inline least-privilege policy: its own
  log group, X-Ray (when tracing is active), its KMS key, the optional SQS
  trigger and its key, and the optional dead-letter target and its key. A
  permissions boundary is always attached (default: the account's
  `iacp-boundary` policy).
- `extra_policy_jsons`, a map of policy documents from other modules keyed by
  stable names. Allow statements may not use `*`, `<service>:*`, non-read
  wildcards, `NotAction` or `NotResource`.
- CloudWatch log group with configurable retention (default 30 days) and KMS
  encryption.
- KMS encryption of environment variables with a module-created key
  (rotation on, scoped Logs service grant) unless `kms_key_arn` is given.
- X-Ray tracing (Active by default, required in production) and reserved
  concurrency (default 10, 1-1000, required in production).
- Optional SQS event source mapping (`sqs_trigger`) with partial batch
  failure reporting, a batching window, and batch size rules that match SQS
  (above 10 only with a window, never on FIFO).
- Mandatory `iacp:*` tags on every taggable resource, including the event
  source mapping. Requires AWS provider >= 5.70.
