## Summary

<!-- What does this change do, and why? Link the WBS task(s) or issue. -->

## Changes

-

## Verification

<!-- Commands you ran and their result, e.g. `make lint`, `opa test policies/opa`, `make module-test`. -->

## Checklist

- [ ] Tests added or updated and passing (unit, golden files, policy tests, Terratest where relevant)
- [ ] `terraform fmt`, TFLint, Checkov and OPA are clean
- [ ] No static credentials, `.env` files, state or plan files committed
- [ ] Module and action refs are pinned (`?ref=modules/<name>/vX.Y.Z`, actions by commit SHA)
- [ ] Docs, ADRs and diagrams updated if behaviour or architecture changed
- [ ] `CHANGELOG.md` (`[Unreleased]`) and `WBS.md` updated
