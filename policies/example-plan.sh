#!/usr/bin/env bash
# Plans a module's examples/complete and runs conftest on the plan JSON with the
# example's policy-context.json (the stack context the platform would supply).
#
# Examples call their module through a relative path ("../…") so they test the
# working tree, which iacp.forbidden.module_source always denies (generated
# stacks use pinned git::…?ref=modules/<name>/vX.Y.Z sources, and that rule
# cannot be waived). Only that deny, for a source starting with "../", is
# expected. Any other deny fails, including module_source for ?ref=main,
# third-party or otherwise unpinned sources. Warnings are printed but do not
# fail.
#
# The plan is never applied. It needs the example's provider endpoints to
# answer STS (aws_caller_identity), which LocalStack community does; planning
# new resources makes no other AWS call.
#
# Usage:
#   policies/example-plan.sh modules/<name>                     (needs terraform, conftest, jq)
#   policies/example-plan.sh --plan plan.json --context ctx.json  (gate an existing plan JSON;
#                                                                  used by fixtures/verify-example-gate.sh)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
example=""
cleanup() {
  rm -rf "$work"
  if [ -n "$example" ]; then rm -f "$example/tfplan"; fi
}
trap cleanup EXIT

if [ "${1:-}" = "--plan" ]; then
  plan="${2:?--plan needs a file}"
  [ "${3:-}" = "--context" ] || { echo "usage: $0 --plan plan.json --context ctx.json" >&2; exit 2; }
  context="${4:?--context needs a file}"
  label="$plan"
else
  module_dir="${1:?usage: $0 modules/<name> | --plan plan.json --context ctx.json}"
  example="$module_dir/examples/complete"
  context="$example/policy-context.json"
  [ -f "$context" ] || { echo "no $context" >&2; exit 1; }

  terraform -chdir="$example" init -backend=false -input=false >/dev/null
  terraform -chdir="$example" plan -input=false -lock=false -refresh=false -out=tfplan >/dev/null
  terraform -chdir="$example" show -json tfplan >"$work/plan.json"
  plan="$work/plan.json"
  label="$example"
fi

result="$(conftest test "$plan" -p "$here/opa" --data "$context" --output json || true)"

messages() { jq -r --arg kind "$1" '.[][$kind][]?.msg' <<<"$result"; }

# The one accepted deny: a module call whose source is a relative "../" path.
relative_source='^\[iacp\.forbidden\.module_source\] module\.[^:]+: source "\.\./'

status=0
while IFS= read -r msg; do
  [ -n "$msg" ] || continue
  if [[ "$msg" =~ $relative_source ]]; then
    echo "  expected  $msg"
  else
    echo "  DENY      $msg" >&2
    status=1
  fi
done < <(messages failures)

while IFS= read -r msg; do
  if [ -n "$msg" ]; then echo "  warn      $msg"; fi
done < <(messages warnings)

if [ "$status" -eq 0 ]; then
  echo "conftest: $label has no denies other than relative module sources"
fi
exit "$status"
