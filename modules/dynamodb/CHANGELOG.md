# Changelog — modules/dynamodb

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/dynamodb/vX.Y.Z`.

## [1.0.0] - 2026-11-26

### Added
- DynamoDB table named `<team>-<environment>-<name>` with a partition key and
  an optional sort key (`S`, `N` or `B`).
- On-demand billing by default; `PROVISIONED` with explicit read and write
  capacity, validated against the billing mode.
- Global secondary indexes from a typed list, with `ALL`, `KEYS_ONLY` or
  `INCLUDE` projections. Attribute definitions are derived from the key
  attributes, and a key attribute must keep one type across the table and its
  indexes.
- Server-side encryption with a module-created key (rotation on) unless
  `kms_key_arn` is given.
- Point-in-time recovery, always on.
- Deletion protection on outside development; turning it off there is
  rejected.
- Optional TTL attribute and optional stream (`stream_arn` output).
- `read_policy_json` and `write_policy_json` least-privilege outputs with
  explicit actions and KMS access limited to DynamoDB (`kms:ViaService`).
- Mandatory `iacp:*` tags on the table and the key.
