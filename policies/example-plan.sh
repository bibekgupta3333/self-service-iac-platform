#!/usr/bin/env bash
# Plans a module's examples/complete and runs conftest on the plan JSON with the
# example's policy-context.json (the stack context the platform would supply).
#
# Examples call their module through a relative path so they test the working
# tree, which iacp.forbidden.module_source always denies (generated stacks use
# pinned git::…?ref=modules/<name>/vX.Y.Z sources, and that rule cannot be
# waived). Those denies are expected and reported; any other deny fails.
# Warnings are printed but do not fail.
#
# The plan is never applied. It needs the example's provider endpoints to
# answer STS (aws_caller_identity), which LocalStack community does; planning
# new resources makes no other AWS call.
#
# Usage: policies/example-plan.sh modules/<name>   (needs terraform, conftest, jq)
set -euo pipefail

module_dir="${1:?usage: $0 modules/<name>}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
example="$module_dir/examples/complete"
context="$example/policy-context.json"

[ -f "$context" ] || { echo "no $context" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"; rm -f "$example/tfplan"' EXIT

terraform -chdir="$example" init -backend=false -input=false >/dev/null
terraform -chdir="$example" plan -input=false -lock=false -refresh=false -out=tfplan >/dev/null
terraform -chdir="$example" show -json tfplan >"$work/plan.json"

result="$(conftest test "$work/plan.json" -p "$here/opa" --data "$context" --output json || true)"

messages() { jq -r --arg kind "$1" '.[][$kind][]?.msg' <<<"$result"; }

status=0
while IFS= read -r msg; do
  [ -n "$msg" ] || continue
  case "$msg" in
    "[iacp.forbidden.module_source] "*) echo "  expected  $msg" ;;
    *) echo "  DENY      $msg" >&2; status=1 ;;
  esac
done < <(messages failures)

while IFS= read -r msg; do
  [ -n "$msg" ] && echo "  warn      $msg"
done < <(messages warnings)

if [ "$status" -eq 0 ]; then
  echo "conftest: $example has no denies other than the relative module source"
fi
exit "$status"
