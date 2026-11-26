# Changelog — modules/lambda

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/lambda/vX.Y.Z`.

## [2.0.0] - 2026-11-26

### Changed (breaking)
- `extra_policy_jsons` now rejects Allow statements that grant `iam:PassRole`
  (any resource, any letter case, and wildcard spellings such as `iam:Pass?ole`,
  `i?m:PassRole` or `iam:Pass*`, because IAM reads `?` as one character) and
  any action that can be an `iam:` action unless its resources name an account
  (`"*"`, `arn:aws:iam::*:*` and a wildcard in the partition, service, region
  or account field are rejected). Actions may only contain letters, digits,
  `:`, `-`, `_`, `*` and `?`. A function
  role that can pass roles to services can escalate beyond its own
  permissions, and the permissions boundary was the only backstop.
  The release gate classifies a stricter validation as MAJOR because it may
  reject values that were valid before.

### Upgrade notes
- No resource changes: this release only adds a validation (the only plan
  difference for a stack that passes it is the `iacp_metadata` output, which
  now reports version 2.0.0).
- A stack that fails the new validation at plan time has an
  `extra_policy_jsons` document with one of:
  - `iam:PassRole` in an Allow statement. Remove it. If a function must hand
    a role to a service, create that resource from a module that owns the
    role instead of from the function's policy.
  - an `iam:` action (for example `iam:GetRole`) with `Resource "*"` or a
    wildcard account. Name the IAM ARNs the function touches, such as
    `arn:aws:iam::<account>:role/iacp/<team>-*`.
- Deny statements are not affected.
- Change `?ref=modules/lambda/v1.0.0` to `?ref=modules/lambda/v2.0.0` once
  the policies pass.

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
