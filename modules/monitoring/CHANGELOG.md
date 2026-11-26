# Changelog — modules/monitoring

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/monitoring/vX.Y.Z`.

## [1.0.0] - 2026-11-26

### Added
- SNS alarm topic named `<team>-<environment>-<name>`, encrypted with a
  module-created key (rotation on, CloudWatch granted for this account only)
  unless `kms_key_arn` is given.
- Topic policy that lets only CloudWatch and `publisher_service_principals`
  publish for this account, and denies publishing without TLS.
- Optional subscriptions keyed by stable names (`email`, `email-json`,
  `https`, `lambda`; plain `http` and `sqs` are rejected). Lambda
  subscriptions get an invoke permission limited to this topic and account.
- CloudWatch dashboard built from typed metric and text widgets, with an alarm
  status widget appended for every alarm the module creates.
- Alarm helpers: SQS age of oldest message and optional depth, Lambda errors
  and throttles, DynamoDB read and write throttle events. Every alarm notifies
  the topic on alarm and on recovery.
- `publisher_policy_json` least-privilege output.
- Mandatory `iacp:*` tags on the topic, the key and every alarm.
