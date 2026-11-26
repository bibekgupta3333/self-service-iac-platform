# Changelog — modules/sqs

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/sqs/vX.Y.Z`.

## [1.0.0] - 2026-11-25

### Added
- Standard or FIFO queue named `<team>-<environment>-<name>`.
- Mandatory dead-letter queue (tagged `iacp:role=dlq`) with redrive policy
  and a `byQueue` redrive allow policy limited to the main queue.
- SSE-KMS on both queues with a module-created key (rotation on) unless
  `kms_key_arn` is given. `key_service_principals` lets AWS services such as
  SNS or EventBridge use that key for this account.
- TLS-only queue policies on both queues.
- CloudWatch alarms for a non-empty DLQ and for the age of the oldest message.
- `consumer_policy_json` and `producer_policy_json` least-privilege outputs.
- Mandatory `iacp:*` tags on every taggable resource.
