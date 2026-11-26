#!/usr/bin/env bash
# Runs conftest against the plan fixtures with the stack context in context.json:
#   - compliant.plan.json must produce no denies and no warnings;
#   - noncompliant.plan.json must be denied by every deny rule ID and warned by
#     every warn rule ID listed below (so each rule is exercised end to end);
#   - compliant.plan.json with context-yanked.json (its pinned modules/s3/v1.2.0
#     listed as yanked, as GET /v1/internal/budget-context would) must be denied
#     by iacp.modules.yanked and by nothing else;
#   - compliant.plan.json with context-budget.json (a team budget, the budget gate
#     on and a cost estimate, as `stack_pipeline budget-context` builds it) and
#     variants of it must give exactly the budget findings listed in the cases
#     below: over budget, near the limit, an override (which production ignores),
#     and a missing estimate (warned in development, denied elsewhere).
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

# budget_case LABEL JQ_PATCH DENY_IDS WARN_IDS: evaluates compliant.plan.json with
# context-budget.json patched by JQ_PATCH (a jq filter over .iacp.context). The
# iacp.budget.* findings must equal DENY_IDS and WARN_IDS (space separated, "-" for
# none). In staging nothing but budget findings may appear: the plan is otherwise clean
# (a production or development context makes the staging-tagged plan mismatch, so other findings are
# tolerated there).
budget_case() {
  local label="$1" patch="$2" want_deny="$3" want_warn="$4" dir ctx res got_deny got_warn other
  dir="$(mktemp -d)"
  ctx="$dir/context.json" # conftest picks --data files by extension
  jq ".iacp.context |= ($patch)" "$here/context-budget.json" >"$ctx"
  res="$(conftest test "$here/compliant.plan.json" -p "$policy" --data "$ctx" --output json || true)"
  rm -rf "$dir"
  pick() {
    jq -r --arg kind "$1" '.[][$kind][]?.msg | capture("^\\[(?<id>iacp\\.budget\\.[^\\]]+)\\]").id' <<<"$res" |
      sort -u | tr '\n' ' ' | sed 's/ $//'
  }
  got_deny="$(pick failures)"
  got_warn="$(pick warnings)"
  [ "$want_deny" = "-" ] && want_deny=""
  [ "$want_warn" = "-" ] && want_warn=""
  other="$(jq -r '.[][("failures","warnings")][]?.msg | select(startswith("[iacp.budget.") | not)' <<<"$res")"
  if [ "$got_deny" = "$want_deny" ] && [ "$got_warn" = "$want_warn" ] &&
    { [ -z "$other" ] || [[ "$patch" == *production* || "$patch" == *development* ]]; }; then
    echo "  ok      $label (deny: ${got_deny:-none}; warn: ${got_warn:-none})"
  else
    echo "  WRONG   $label: deny [$got_deny] want [$want_deny]; warn [$got_warn] want [$want_warn]; other: ${other:-none}" >&2
    status=1
  fi
}

override='{granted_by: "ada", reason: "launch week capacity", expires_at: "2200-01-01T00:00:00Z"}'
expired='{granted_by: "ada", reason: "launch week capacity", expires_at: "2026-11-01T00:00:00Z"}'

echo "conftest: compliant.plan.json against a team budget (1500 of 2000 used)"
budget_case "600 would exceed the hard budget" '.' iacp.budget.exceeded -
budget_case "100 is near the limit (80 percent)" '.estimated_monthly_cost_usd = 100' - iacp.budget.near_limit
budget_case "50 is comfortably within it" '.estimated_monthly_cost_usd = 50' - -
budget_case "a soft budget only warns" '.budget.hard_block = false' - iacp.budget.exceeded_waived
budget_case "an override waives it in staging" ".budget_override = $override" - iacp.budget.exceeded_waived
budget_case "an expired override does not" ".budget_override = $expired" iacp.budget.exceeded -
budget_case "production ignores the override" ".environment = \"production\" | .budget_override = $override" iacp.budget.exceeded -
budget_case "no estimate is denied in staging" 'del(.estimated_monthly_cost_usd)' iacp.budget.no_estimate -
budget_case "a forged non-numeric estimate counts as none" '.estimated_monthly_cost_usd = "0"' iacp.budget.no_estimate -
budget_case "no estimate only warns in development" '.environment = "development" | del(.estimated_monthly_cost_usd)' - iacp.budget.no_estimate
budget_case "an override waives a missing estimate in staging" "del(.estimated_monthly_cost_usd) | .budget_override = $override" - iacp.budget.no_estimate
budget_case "no estimate outside the gate is not checked" 'del(.estimated_monthly_cost_usd) | del(.budget_gate)' - -
budget_case "no budget configured, nothing to enforce" 'del(.budget) | del(.estimated_monthly_cost_usd)' - -

exit "$status"
