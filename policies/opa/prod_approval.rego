# METADATA
# title: Pre-apply approval gate
# description: |
#   The backstop apply.yml evaluates on the plan it is about to apply (docs/07 6.8,
#   WBS 8.8). Branch protection, CODEOWNERS and the production GitHub Environment enforce
#   approvals natively; this package re-checks them from main's code, so a misconfigured
#   protection rule cannot let an unapproved production change through.
#
#   Input: the pre-apply envelope built by `tools.stack_pipeline preapply-input`, never a
#   bare plan. Every rule here applies only to that envelope (input.kind ==
#   "iacp.preapply"), so the conftest `main` namespace, which aggregates every iacp.*
#   package over a plain plan in pr-validate and apply, never sees an approval finding.
#
#     kind        "iacp.preapply"
#     plan        the plan JSON (`terraform show -json tfplan`) about to be applied
#     plan_hash   the normalised hash of that plan (tools.stack_pipeline summary)
#     request
#       author                  GitHub login of the request's author (from the API), or ""
#       pr_author               GitHub login of the pull request's author
#       approved_plan_hash      the hash pinned at approval (API), or ""
#       allow_stateful_destroy  the request's flag (API); false when unknown
#       approvals               the latest review of each reviewer of the pull request:
#                               {login, state, role, on_head}. role is "maintainer" or
#                               "admin" only when the reviewer owns the stack root in main's
#                               CODEOWNERS (or is a platform owner), otherwise "none".
#                               on_head is true when the review is on the PR's final commit.
#
#   The environment comes from data.iacp.context.environment. Anything other than
#   development or staging (an unknown or missing environment included) is held to the
#   production rules (fail closed). None of these rules is waivable: they never call
#   lib.iacp.exempt, so no policy exception can lift them.
package iacp.approval

import data.lib.iacp as f

# Machine identities never count as an approver in production (docs/07 section 7).
machine_users := {"iacp-approver", "iacp-bot", "github-actions[bot]"}

envelope if input.kind == "iacp.preapply"

env := f.ctx_value("environment", "unknown")

production if not env in {"development", "staging"}

request := object.get(input, "request", {})

lower_or_empty(x) := lower(x) if is_string(x)

lower_or_empty(x) := "" if not is_string(x)

author := lower_or_empty(object.get(request, "author", ""))

pr_author := lower_or_empty(object.get(request, "pr_author", ""))

machine(login) if login in machine_users

machine(login) if endswith(login, "[bot]")

# maintainer_approvals: distinct logins whose latest review approves the PR's final commit,
# who own the stack root (maintainer) or the platform (admin), and who are neither the
# request's author, the PR's author nor a machine user.
maintainer_approvals contains login if {
	some a in object.get(request, "approvals", [])
	a.state == "APPROVED"
	a.role in {"maintainer", "admin"}
	a.on_head == true
	login := lower_or_empty(a.login)
	login != ""
	login != author
	login != pr_author
	not machine(login)
}

plan := object.get(input, "plan", {})

deleted contains rc if {
	some rc in object.get(plan, "resource_changes", [])
	rc.mode == "managed"
	"delete" in rc.change.actions
}

destructive if count(deleted) > 0

approved_hash := h if {
	h := object.get(request, "approved_plan_hash", "")
	is_string(h)
	h != ""
}

stack := f.ctx_value("stack_path", "this stack")

# --- The envelope itself --------------------------------------------------------------

has_plan if is_object(input.plan)

deny contains m if {
	envelope
	not has_plan
	m := f.msg(
		"iacp.approval.input", stack, "the pre-apply input carries no plan",
		"build it with tools.stack_pipeline preapply-input from the plan about to be applied",
	)
}

# --- Approvals ------------------------------------------------------------------------

deny contains m if {
	envelope
	production
	count(maintainer_approvals) < 1
	m := f.msg(
		"iacp.approval.prod_min", stack,
		sprintf("a %s change needs an approval of its final commit by at least one maintainer of the stack who is not its author; found none", [env]),
		"ask a code owner of the stack root (CODEOWNERS) to approve the pull request, then re-run the apply",
	)
}

deny contains m if {
	envelope
	production
	destructive
	count(maintainer_approvals) < 2
	m := f.msg(
		"iacp.approval.prod_destructive", stack,
		sprintf("the plan deletes or replaces %d resource(s), which in %s needs approvals from two maintainers; found %d", [count(deleted), env, count(maintainer_approvals)]),
		"ask a second code owner of the stack root to approve the pull request",
	)
}

# --- What is applied is what was approved ----------------------------------------------

deny contains m if {
	envelope
	approved_hash
	approved_hash != object.get(input, "plan_hash", "")
	m := f.msg(
		"iacp.approval.plan_hash", stack,
		sprintf("the plan about to be applied (%v) differs from the approved plan (%s)", [object.get(input, "plan_hash", null), approved_hash]),
		"re-run validation on the pull request so the new plan is reviewed and approved",
	)
}

deny contains m if {
	envelope
	production
	not approved_hash
	m := f.msg(
		"iacp.approval.plan_hash_missing", stack,
		sprintf("no approved plan hash is recorded, so a %s plan cannot be verified against what was reviewed", [env]),
		"apply production only with the Platform API configured (vars.IACP_API_URL), through an approved request",
	)
}

# --- Stateful resources -----------------------------------------------------------------

stateful := {
	"aws_db_instance", "aws_rds_cluster", "aws_s3_bucket", "aws_elasticache_replication_group",
	"aws_dynamodb_table", "aws_kms_key", "aws_efs_file_system", "aws_secretsmanager_secret",
	"aws_ecr_repository", "aws_sqs_queue", "aws_kinesis_stream",
}

deny contains m if {
	envelope
	production
	object.get(request, "allow_stateful_destroy", false) != true
	some rc in deleted
	rc.type in stateful
	m := f.msg(
		"iacp.approval.stateful_destroy", rc.address,
		sprintf("this %s would be deleted (or replaced) in %s, and the request does not allow stateful deletes", [rc.type, env]),
		"if the data loss is intended, a maintainer re-submits the request with allow_stateful_destroy: true",
	)
}
