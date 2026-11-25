# Changelog — modules/lambda

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/lambda/vX.Y.Z`.

## [1.0.0] - 2026-11-25

### Added
- Lambda function named `<team>-<environment>-<name>`, deployed from an
  `s3://` package built by the application's CI.
- Execution role with an inline least-privilege policy: its own log group,
  X-Ray (when tracing is active), its KMS key, the optional SQS trigger and
  the optional dead-letter target. Optional permissions boundary.
- `extra_policy_jsons` to attach policy documents from other modules, with
  wildcard actions rejected.
- CloudWatch log group with configurable retention (default 30 days) and KMS
  encryption.
- KMS encryption of environment variables with a module-created key
  (rotation on, scoped Logs service grant) unless `kms_key_arn` is given.
- X-Ray tracing (Active by default) and reserved concurrency (default 10,
  required in production).
- Optional SQS event source mapping (`sqs_trigger`) with partial batch
  failure reporting.
- Mandatory `iacp:*` tags on every taggable resource.
