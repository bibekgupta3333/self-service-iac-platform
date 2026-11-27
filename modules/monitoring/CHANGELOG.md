# Changelog — modules/monitoring

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/monitoring/vX.Y.Z`.

## [1.1.0] - 2026-11-26

### Added
- `metric_alarms`: an optional list of alarms on any CloudWatch metric, such
  as a custom `IACP/Platform` metric. Each entry names its alarm with a stable,
  unique `name` and sets the namespace, metric name, dimensions, statistic
  (basic or a percentile such as `p95`, sent as the extended statistic),
  threshold, comparison, period, evaluation periods and missing-data
  treatment. Every field is validated at plan time. The alarms notify the
  module's topic on alarm and on recovery, carry the platform tags, appear in
  `alarm_arns` as `metric-<name>` and on the dashboard's alarm status widget.

### Upgrade notes
- Backward compatible: the default is an empty list, and the helper alarms
  keep one 5-minute period, their statistic and `notBreaching`. A stack that
  does not set `metric_alarms` plans no change besides the `iacp_metadata`
  output, which now reports version 1.1.0.
- Change `?ref=modules/monitoring/v1.0.0` to `?ref=modules/monitoring/v1.1.0`.

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
