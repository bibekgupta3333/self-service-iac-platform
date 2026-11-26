#!/usr/bin/env bash
# Runs conftest against the plan fixtures with the stack context in context.json:
#   - compliant.plan.json must produce no denies and no warnings;
#   - noncompliant.plan.json must be denied by every deny rule ID and warned by
#     every warn rule ID listed below (so each rule is exercised end to end);
#   - compliant.plan.json with context-yanked.json (its pinned modules/s3/v1.2.0
#     listed as yanked, as GET /v1/internal/budget-context would) must be denied
#     by iacp.modules.yanked and by nothing else.
#
# Usage: policies/fixtures/verify.sh   (needs conftest and jq)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
policy="$here/../opa"
context="$here/context.json"

expected_denies=(
  iacp.ecr.immutable_tags
  iacp.ecr.scan_on_push
  iacp.encryption.dynamodb_cmk
  iacp.encryption.ecr_kms
  iacp.encryption.elasticache_at_rest
  iacp.encryption.elasticache_cmk
  iacp.encryption.elasticache_transit
  iacp.encryption.kms_rotation
  iacp.encryption.rds_cmk
  iacp.encryption.rds_storage
  iacp.encryption.s3_cmk
  iacp.encryption.s3_kms
  iacp.encryption.sns_kms
  iacp.encryption.sns_sse
  iacp.encryption.sqs_kms
  iacp.encryption.sqs_sse
  iacp.forbidden.data_source
  iacp.forbidden.force_destroy
  iacp.forbidden.module_source
  iacp.forbidden.provisioner
  iacp.iam.admin_policy
  iacp.iam.not_resource
  iacp.iam.wildcard_action
  iacp.iam.wildcard_resource
  iacp.network.ecs_public_ip
  iacp.network.rds_public
  iacp.placement.region
  iacp.placement.region_missing
  iacp.s3.no_public
  iacp.s3.no_public_acl
  iacp.s3.no_public_policy
  iacp.tags.mismatch
  iacp.tags.required
)

expected_warnings=(
  iacp.iam.full_access_policy
  iacp.iam.policy_unknown
  iacp.placement.region_unverified
  iacp.s3.policy_unknown
)

echo "conftest: compliant.plan.json (expect no denies or warnings)"
conftest test "$here/compliant.plan.json" -p "$policy" --data "$context" --fail-on-warn

echo "conftest: noncompliant.plan.json (expect every rule)"
result="$(conftest test "$here/noncompliant.plan.json" -p "$policy" --data "$context" --output json || true)"

ids() { jq -r --arg kind "$1" '.[][$kind][]?.msg | capture("^\\[(?<id>[^\\]]+)\\]").id' <<<"$result" | sort -u; }

status=0
check() {
  local kind="$1" actual="$2"
  shift 2
  for id in "$@"; do
    if grep -qx "$id" <<<"$actual"; then
      echo "  ok      $kind $id"
    else
      echo "  MISSING $kind $id" >&2
      status=1
    fi
  done
}

check failures "$(ids failures)" "${expected_denies[@]}"
check warnings "$(ids warnings)" "${expected_warnings[@]}"

echo "conftest: compliant.plan.json with its module version yanked (expect iacp.modules.yanked only)"
result="$(conftest test "$here/compliant.plan.json" -p "$policy" --data "$here/context-yanked.json" --output json || true)"
yanked="$(ids failures)"
check failures "$yanked" iacp.modules.yanked
if [ "$yanked" != "iacp.modules.yanked" ]; then
  echo "  UNEXPECTED failures: $(tr '\n' ' ' <<<"$yanked")" >&2
  status=1
fi
exit "$status"
