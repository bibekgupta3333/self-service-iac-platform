#!/usr/bin/env bash
# Proves that policies/example-plan.sh only waives iacp.forbidden.module_source
# for relative "../" sources:
#   - relative.plan.json    (only ../ sources)              must pass;
#   - ref-main.plan.json    (a platform source on ?ref=main) must fail;
#   - third-party.plan.json (a public registry module)       must fail.
#
# Usage: policies/fixtures/verify-example-gate.sh   (needs conftest and jq)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gate="$here/../example-plan.sh"
dir="$here/example-gate"
context="$here/context.json"

status=0
expect() {
  local want="$1" plan="$2" got=pass
  "$gate" --plan "$dir/$plan" --context "$context" >/dev/null 2>&1 || got=fail
  if [ "$got" = "$want" ]; then
    echo "  ok      $plan ($want)"
  else
    echo "  WRONG   $plan: expected $want, got $got" >&2
    status=1
  fi
}

echo "example-plan gate: only relative module sources are waived"
expect pass relative.plan.json
expect fail ref-main.plan.json
expect fail third-party.plan.json
exit "$status"
