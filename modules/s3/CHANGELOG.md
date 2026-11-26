# Changelog — modules/s3

All notable changes to this module. Versions follow SemVer and are released
as git tags `modules/s3/vX.Y.Z`.

## [1.0.0] - 2026-11-25

### Added
- Private S3 bucket named `<team>-<environment>-<name>`.
- SSE-KMS with bucket keys. Uses a module-created key (rotation on, alias
  `alias/iacp/s3/<name>`) unless `kms_key_arn` is given.
- Public access block (all four flags), `BucketOwnerEnforced` ownership and a
  bucket policy that denies non-TLS requests and TLS older than 1.2.
- `additional_policy_statements` for caller grants (no wildcard principals
  in Allow statements) and `key_service_principals` for AWS services that
  must use the module-created key.
- Versioning (always on in production; `Disabled` for new unversioned
  buckets), and one lifecycle rule for noncurrent-version expiry, optional
  STANDARD_IA transition and multipart-upload cleanup.
- Optional server access logging to an existing bucket.
- `read_policy_json` and `write_policy_json` least-privilege policy outputs.
- Mandatory `iacp:*` tags on every taggable resource.
