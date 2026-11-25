#!/usr/bin/env bash
# Runs conftest against the plan fixtures:
#   - compliant.plan.json must produce no denies;
#   - noncompliant.plan.json must be denied by every rule id listed below.
#
# Usage: policies/fixtures/verify.sh   (needs conftest and jq)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
policy="$here/../opa"

expected_rule_ids=(
  iacp.encryption.kms_rotation
  iacp.encryption.s3_kms
  iacp.encryption.sqs_kms
  iacp.encryption.sqs_sse
  iacp.forbidden.force_destroy
  iacp.forbidden.module_source
  iacp.iam.wildcard_resource
  iacp.placement.region
  iacp.s3.no_public
  iacp.s3.no_public_policy
  iacp.tags.required
)

echo "conftest: compliant.plan.json (expect pass)"
conftest test "$here/compliant.plan.json" -p "$policy"

echo "conftest: noncompliant.plan.json (expect denies)"
result="$(conftest test "$here/noncompliant.plan.json" -p "$policy" --output json || true)"
actual="$(jq -r '.[].failures[]?.msg | capture("^\\[(?<id>[^\\]]+)\\]").id' <<<"$result" | sort -u)"

status=0
for id in "${expected_rule_ids[@]}"; do
  if grep -qx "$id" <<<"$actual"; then
    echo "  ok      $id"
  else
    echo "  MISSING $id" >&2
    status=1
  fi
done
exit "$status"
