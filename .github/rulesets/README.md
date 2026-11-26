# GitHub rulesets and Environments (WBS 6.6, applied in 2.5)

These files are the reviewed, checked-in settings for branch protection and deployment
Environments. Nothing applies them automatically: the repository owner applies them with
`gh api` as part of WBS 2.5 (GitHub setup), and re-applies them after any change here.
CODEOWNERS itself is generated from `.github/teams.yaml` by `python -m tools.codeowners`
(CI fails on drift).

| File | API | What it enforces |
|------|-----|------------------|
| `main-branch.json` | `POST /repos/{owner}/{repo}/rulesets` | On the default branch: no deletion or force-push; every change through a PR with one approval, **code-owner review**, stale approvals dismissed on push, the last pusher cannot be the approver, conversations resolved, merge commits only (AGENTS.md); required checks from `ci.yml` and `pr-validate.yml`, branch up to date. No bypass actors, the platform GitHub App included (docs/07 §3). |
| `environment-staging.json` | `PUT /repos/{owner}/{repo}/environments/staging` | Deployments only from protected branches (`main`). |
| `environment-production.json` | `PUT /repos/{owner}/{repo}/environments/production` | Deployments only from `main`, a required reviewer, and no self-approval of a run you triggered. Replace the placeholder reviewer id first (below). |

Required status checks are job names. A job skipped by its `detect` gate reports
`skipped`, which satisfies the check, so the list can include jobs that do not run on
every PR. Keep the list in step with the `name:` of the jobs in `.github/workflows/`:
`make required-checks-check` (`python -m tools.required_checks --check`, also a pytest in CI) fails
when the list is empty, when a required check is not the name of a job (`*.yml` and `*.yaml`), or
when two jobs share a required name. `ci-result` is the aggregate of `ci.yml`: a job skipped because
`detect` failed counts as passing, so `ci-result` fails unless `detect` succeeded. Not required on purpose: `detect` and the
`code scanning` upload jobs (non-gating), and the `cost`, `cost summary`, `comment` and `report`
jobs of `pr-validate` (informational; `pr-validate-result` is the one check for that workflow).

## Apply

```bash
repo=bibekgupta3333/self-service-iac-platform

# Branch ruleset (create once; afterwards update it by id).
gh api -X POST "repos/$repo/rulesets" --input .github/rulesets/main-branch.json
id=$(gh api "repos/$repo/rulesets" --jq '.[] | select(.name == "main") | .id')
gh api -X PUT "repos/$repo/rulesets/$id" --input .github/rulesets/main-branch.json

# Environments. Production needs the numeric id of each reviewer (a user, or a team in
# an organisation: {"type": "Team", "id": <team id>}).
gh api -X PUT "repos/$repo/environments/staging" --input .github/rulesets/environment-staging.json
reviewer=$(gh api users/bibekgupta3333 --jq .id)
jq --argjson id "$reviewer" '.reviewers = [{"type": "User", "id": $id}]' \
  .github/rulesets/environment-production.json |
  gh api -X PUT "repos/$repo/environments/production" --input -

# Verify.
gh api "repos/$repo/rulesets/$id" --jq '{enforcement, rules: [.rules[].type]}'
gh api "repos/$repo/environments/production" --jq '{protection_rules, deployment_branch_policy}'
```

## Notes

- **Single maintainer.** With one owner, `require_last_push_approval` and production's
  `prevent_self_review` mean a change pushed or triggered by the owner needs a second
  account to approve it. Until there is one, the owner can lower
  `required_approving_review_count` to 0 locally before applying; code-owner review then
  still applies to PRs opened by anyone else. Do not commit that change.
- **Organisations.** In an organisation, set `org` in `.github/teams.yaml` so team stack
  roots default to `@<org>/<team>`, make production use `@<org>/<team>-maintainers`
  (`teams.<slug>.environments.production`), and use Team reviewers on the production
  Environment, excluding `@<org>/self-service-iac-platform-approvers` (docs/07 §7).
- Rulesets need a public repository or a paid plan for private ones.
