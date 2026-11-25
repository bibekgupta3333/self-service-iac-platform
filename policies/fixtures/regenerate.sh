#!/usr/bin/env bash
# Regenerates the plan JSON fixtures used by `conftest test` in CI.
#
# Plans run offline against dummy credentials (planning brand-new resources
# makes no AWS calls) and are never applied. The module source is rewritten in
# the JSON afterwards because the pinned release tags may not exist yet.
#
# Usage: policies/fixtures/regenerate.sh   (needs terraform and jq)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="git::https://github.com/bibekgupta3333/self-service-iac-platform.git"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

generate() {
  local name="$1" source="$2"
  rm -rf "${work:?}"/*
  cp -R "$here/src/." "$work/"
  terraform -chdir="$work/$name" init -input=false -backend=false >/dev/null
  terraform -chdir="$work/$name" plan -input=false -refresh=false -out=tfplan >/dev/null
  terraform -chdir="$work/$name" show -json tfplan |
    jq --arg src "$source" '
      .configuration.root_module.module_calls.store.source = $src
      | del(.timestamp)' >"$here/$name.plan.json"
  echo "wrote $here/$name.plan.json"
}

generate compliant "${repo}//modules/s3?ref=modules/s3/v1.2.0"
generate noncompliant "${repo}//modules/s3?ref=main"
