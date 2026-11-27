package iacp.approval_test

import data.iacp.approval
import data.lib.testing as t
import data.main

hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

other_hash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

prod := object.union(t.prod_ctx, {"stack_path": "environments/production/payments/orders"})

staging := {"environment": "staging", "team": "payments", "stack": "orders"}

queue := t.rc("aws_sqs_queue", "q", {"tags_all": t.good_tags})

topic := t.rc("aws_sns_topic", "t", {"tags_all": t.good_tags})

review(login, role) := {"login": login, "state": "APPROVED", "role": role, "on_head": true}

envelope(rcs, approvals) := {
	"kind": "iacp.preapply",
	"plan": t.plan(rcs),
	"plan_hash": hash,
	"request": {
		"author": "ada",
		"pr_author": "iacp-bot",
		"approved_plan_hash": hash,
		"allow_stateful_destroy": false,
		"approvals": approvals,
	},
}

with_request(env, fields) := object.union(env, {"request": object.union(env.request, fields)})

ids(denies) := {regex.find_n(`^\[[a-z0-9._]+\]`, m, 1)[0] | some m in denies}

# --- Approvals ----------------------------------------------------------------------

test_production_with_one_maintainer_allowed if {
	count(approval.deny) == 0 with input as envelope([queue], [review("bob", "maintainer")])
		with data.iacp.context as prod
}

test_production_admin_counts if {
	count(approval.deny) == 0 with input as envelope([queue], [review("root", "admin")])
		with data.iacp.context as prod
}

test_production_without_approval_denied if {
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [])
		with data.iacp.context as prod
}

test_message_names_rule_stack_and_fix if {
	some m in approval.deny with input as envelope([queue], []) with data.iacp.context as prod
	startswith(m, "[iacp.approval.prod_min] environments/production/payments/orders: ")
	contains(m, "Fix: ask a code owner")
}

test_author_approval_does_not_count if {
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [review("Ada", "maintainer")])
		with data.iacp.context as prod
}

test_pr_author_approval_does_not_count if {
	inp := with_request(envelope([queue], [review("carol", "maintainer")]), {"pr_author": "carol"})
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as inp with data.iacp.context as prod
}

test_machine_user_never_counts if {
	every login in ["iacp-approver", "iacp-bot", "renovate[bot]"] {
		ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [review(login, "maintainer")])
			with data.iacp.context as prod
	}
}

test_developer_approval_does_not_count if {
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [review("dan", "none")])
		with data.iacp.context as prod
}

test_stale_approval_does_not_count if {
	stale := object.union(review("bob", "maintainer"), {"on_head": false})
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [stale])
		with data.iacp.context as prod
}

test_changes_requested_does_not_count if {
	cr := object.union(review("bob", "maintainer"), {"state": "CHANGES_REQUESTED"})
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [cr])
		with data.iacp.context as prod
}

test_unknown_environment_is_production if {
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [])
		with data.iacp.context as {"environment": "qa"}
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], [])
}

test_staging_needs_no_approval_here if {
	count(approval.deny) == 0 with input as envelope([queue], []) with data.iacp.context as staging
}

# --- Destructive changes -----------------------------------------------------------------

test_destructive_needs_two_maintainers if {
	del := t.with_actions(topic, ["delete"])
	ids(approval.deny) == {"[iacp.approval.prod_destructive]"} with input as envelope([del], [review("bob", "maintainer")])
		with data.iacp.context as prod
}

test_destructive_with_two_maintainers_allowed if {
	del := t.with_actions(topic, ["delete", "create"])
	inp := envelope([del], [review("bob", "maintainer"), review("erin", "maintainer")])
	count(approval.deny) == 0 with input as inp with data.iacp.context as prod
}

test_same_reviewer_twice_counts_once if {
	del := t.with_actions(topic, ["delete"])
	inp := envelope([del], [review("bob", "maintainer"), review("BOB", "maintainer")])
	ids(approval.deny) == {"[iacp.approval.prod_destructive]"} with input as inp with data.iacp.context as prod
}

test_destructive_in_staging_allowed if {
	del := t.with_actions(topic, ["delete"])
	count(approval.deny) == 0 with input as envelope([del], []) with data.iacp.context as staging
}

# --- Plan hash ---------------------------------------------------------------------------

test_plan_hash_mismatch_denied_everywhere if {
	inp := with_request(envelope([queue], [review("bob", "maintainer")]), {"approved_plan_hash": other_hash})
	ids(approval.deny) == {"[iacp.approval.plan_hash]"} with input as inp with data.iacp.context as prod
	ids(approval.deny) == {"[iacp.approval.plan_hash]"} with input as inp with data.iacp.context as staging
}

test_plan_hash_missing_denied_in_production if {
	inp := with_request(envelope([queue], [review("bob", "maintainer")]), {"approved_plan_hash": ""})
	ids(approval.deny) == {"[iacp.approval.plan_hash_missing]"} with input as inp with data.iacp.context as prod
}

test_plan_hash_missing_allowed_outside_production if {
	inp := with_request(envelope([queue], []), {"approved_plan_hash": ""})
	count(approval.deny) == 0 with input as inp with data.iacp.context as staging
}

# --- Stateful deletes ----------------------------------------------------------------------

test_stateful_delete_denied_without_flag if {
	db := t.with_actions(t.rc("aws_db_instance", "main", {}), ["delete"])
	inp := envelope([db], [review("bob", "maintainer"), review("erin", "maintainer")])
	some m in approval.deny with input as inp with data.iacp.context as prod
	startswith(m, "[iacp.approval.stateful_destroy] aws_db_instance.main: ")
	ids(approval.deny) == {"[iacp.approval.stateful_destroy]"} with input as inp with data.iacp.context as prod
}

test_stateful_delete_allowed_with_flag if {
	db := t.with_actions(t.rc("aws_db_instance", "main", {}), ["delete"])
	inp := with_request(
		envelope([db], [review("bob", "maintainer"), review("erin", "maintainer")]),
		{"allow_stateful_destroy": true},
	)
	count(approval.deny) == 0 with input as inp with data.iacp.context as prod
}

test_stateful_replace_denied if {
	b := t.with_actions(t.rc("aws_s3_bucket", "b", {}), ["create", "delete"])
	inp := envelope([b], [review("bob", "maintainer"), review("erin", "maintainer")])
	ids(approval.deny) == {"[iacp.approval.stateful_destroy]"} with input as inp with data.iacp.context as prod
}

test_stateful_delete_allowed_in_staging if {
	db := t.with_actions(t.rc("aws_db_instance", "main", {}), ["delete"])
	count(approval.deny) == 0 with input as envelope([db], []) with data.iacp.context as staging
}

# --- Envelope only ---------------------------------------------------------------------------

test_envelope_without_plan_denied if {
	inp := {"kind": "iacp.preapply", "plan_hash": hash, "request": {"approved_plan_hash": hash}}
	"[iacp.approval.input]" in ids(approval.deny) with input as inp with data.iacp.context as staging
}

test_plain_plan_is_not_judged if {
	count(approval.deny) == 0 with input as t.plan([t.with_actions(topic, ["delete"])]) with data.iacp.context as prod
}

test_main_namespace_ignores_approval_on_plans if {
	q := t.rc("aws_sqs_queue", "q", {"kms_master_key_id": "alias/k", "tags_all": t.good_tags})
	count(main.deny) == 0 with input as t.plan([q])
}

test_exceptions_cannot_waive_approval if {
	exc := object.union(prod, {
		"now": "2026-11-26T00:00:00Z",
		"exceptions": [{"rule_id": "iacp.approval.prod_min", "resource_address": null, "expires_at": "2027-01-01T00:00:00Z"}],
	})
	ids(approval.deny) == {"[iacp.approval.prod_min]"} with input as envelope([queue], []) with data.iacp.context as exc
}
