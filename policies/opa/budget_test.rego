package iacp.budget_test

import data.iacp.budget
import data.lib.testing as t

now := "2026-11-24T10:05:00Z"

future := "2026-11-30T00:00:00Z"

past := "2026-11-01T00:00:00Z"

# A 2000 USD hard budget with 1500 used; estimates come with the gate switched on.
base(env) := {
	"environment": env,
	"team": "payments",
	"stack": "orders",
	"stack_path": sprintf("environments/%s/payments/orders", [env]),
	"now": now,
	"budget_gate": true,
	"budget": {"month": "2026-11", "limit_usd": 2000, "spent_usd": 1500, "actual_usd": null, "hard_block": true, "scope": "team"},
}

with_estimate(ctx, usd) := object.union(ctx, {"estimated_monthly_cost_usd": usd})

# replace_budget swaps the whole budget object (object.union would merge into it).
replace_budget(ctx, b) := object.union(object.remove(ctx, ["budget"]), {"budget": b})

with_override(ctx, expires) := object.union(ctx, {"budget_override": {"granted_by": "ada", "reason": "launch week capacity", "expires_at": expires}})

with_budget(ctx, patch) := object.union(ctx, {"budget": object.union(ctx.budget, patch)})

create_plan := t.plan([t.rc("aws_sqs_queue", "a", {})])

destroy_plan := t.plan([t.with_actions(t.rc("aws_sqs_queue", "a", {}), ["delete"])])

noop_plan := t.plan([t.with_actions(t.rc("aws_sqs_queue", "a", {}), ["no-op"])])

ids(ms) := t.rule_ids(ms)

# --- exceeding the budget ---------------------------------------------------------

test_over_budget_denied_in_every_environment if {
	every env in ["development", "staging", "production"] {
		ms := budget.deny with input as create_plan with data.iacp.context as with_estimate(base(env), 600)
		ids(ms) == {"[iacp.budget.exceeded]"}
	}
}

test_deny_message_names_stack_numbers_and_fix if {
	ms := budget.deny with input as create_plan with data.iacp.context as with_estimate(base("staging"), 600)
	some m in ms
	m == "[iacp.budget.exceeded] environments/staging/payments/orders: estimated cost 600.00 USD/month would raise payments's spend for 2026-11 to 2100.00 USD, over its 2000.00 USD budget (1500.00 used already). Fix: reduce the stack's cost or destroy unused stacks, or ask a team admin to raise the budget (PUT /v1/teams/payments/budgets/2026-11) or, outside production, to grant an override"
}

test_exactly_at_the_limit_passes if {
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as with_estimate(base("staging"), 500)
	count(budget.warn) == 1 with input as create_plan with data.iacp.context as with_estimate(base("staging"), 500)
}

test_actual_above_estimate_counts_as_used if {
	ctx := with_estimate(with_budget(base("staging"), {"spent_usd": 100, "actual_usd": 1900}), 150)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

test_actual_below_estimate_does_not_lower_used if {
	ctx := with_estimate(with_budget(base("staging"), {"spent_usd": 1900, "actual_usd": 100}), 150)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

test_zero_limit_denies_any_cost if {
	ctx := with_estimate(with_budget(base("staging"), {"limit_usd": 0, "spent_usd": 0}), 0.01)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

# --- soft budgets and the warn threshold --------------------------------------------

test_soft_budget_only_warns if {
	ctx := with_estimate(with_budget(base("production"), {"hard_block": false}), 600)
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
	ids(budget.warn) == {"[iacp.budget.exceeded_waived]"} with input as create_plan with data.iacp.context as ctx
}

test_missing_hard_block_flag_is_a_hard_block if {
	ctx := with_estimate(replace_budget(base("staging"), {"limit_usd": 2000, "spent_usd": 1500}), 600)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

test_warn_from_the_default_threshold if {
	# 1500 + 100 = 1600 = 80 percent of 2000
	ms := budget.warn with input as create_plan with data.iacp.context as with_estimate(base("staging"), 100)
	ids(ms) == {"[iacp.budget.near_limit]"}
	some m in ms
	contains(m, "80% of its 2000.00 USD budget")
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as with_estimate(base("staging"), 100)
}

test_below_the_threshold_is_quiet if {
	count(budget.warn) == 0 with input as create_plan with data.iacp.context as with_estimate(base("staging"), 99.99)
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as with_estimate(base("staging"), 99.99)
}

test_warn_threshold_is_configurable if {
	ctx := with_estimate(with_budget(base("staging"), {"warn_pct": 90}), 100)
	count(budget.warn) == 0 with input as create_plan with data.iacp.context as ctx
	ids(budget.warn) == {"[iacp.budget.near_limit]"} with input as create_plan with data.iacp.context as with_estimate(with_budget(base("staging"), {"warn_pct": 90}), 300)
}

# --- the override ---------------------------------------------------------------------

test_override_waives_the_hard_limit_outside_production if {
	every env in ["development", "staging"] {
		ctx := with_override(with_estimate(base(env), 600), future)
		count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
		ms := budget.warn with input as create_plan with data.iacp.context as ctx
		ids(ms) == {"[iacp.budget.exceeded_waived]"}
		some m in ms
		contains(m, "an admin override granted by ada is in effect")
	}
}

test_override_cannot_waive_production if {
	ctx := with_override(with_estimate(base("production"), 600), future)
	ms := budget.deny with input as create_plan with data.iacp.context as ctx
	ids(ms) == {"[iacp.budget.exceeded]"}
	some m in ms
	contains(m, "an admin override does not apply in production")
}

test_unknown_environment_is_treated_as_production if {
	ctx := with_override(with_estimate(object.remove(base("staging"), ["environment"]), 600), future)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

test_expired_override_does_not_apply if {
	ctx := with_override(with_estimate(base("staging"), 600), past)
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

test_override_expiry_uses_the_context_clock if {
	# expires after the context's now, so it is effective whatever the wall clock says
	ctx := with_override(with_estimate(base("staging"), 600), "2200-01-01T00:00:00Z")
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
}

test_policy_exceptions_cannot_waive_the_budget if {
	exc := [{"rule_id": "iacp.budget.exceeded", "resource_address": null, "expires_at": "2200-01-01T00:00:00Z"}]
	ctx := object.union(with_estimate(base("staging"), 600), {"exceptions": exc})
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as create_plan with data.iacp.context as ctx
}

# --- destroys and no-ops ------------------------------------------------------------------

test_destroy_always_passes_even_when_over_budget if {
	ctx := with_estimate(with_budget(base("production"), {"spent_usd": 5000}), 0)
	count(budget.deny) == 0 with input as destroy_plan with data.iacp.context as ctx
	count(budget.warn) == 0 with input as destroy_plan with data.iacp.context as ctx
}

test_destroy_passes_even_with_a_stale_positive_estimate if {
	ctx := with_estimate(base("production"), 9000)
	count(budget.deny) == 0 with input as destroy_plan with data.iacp.context as ctx
}

test_destroy_passes_without_an_estimate if {
	count(budget.deny) == 0 with input as destroy_plan with data.iacp.context as base("production")
}

test_noop_plan_adds_nothing if {
	count(budget.deny) == 0 with input as noop_plan with data.iacp.context as with_estimate(base("staging"), 9000)
}

test_replacement_counts_as_a_change if {
	replace := t.plan([t.with_actions(t.rc("aws_sqs_queue", "a", {}), ["delete", "create"])])
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as replace with data.iacp.context as with_estimate(base("staging"), 600)
}

test_unknown_plan_is_not_assumed_to_free_budget if {
	ids(budget.deny) == {"[iacp.budget.exceeded]"} with input as {} with data.iacp.context as with_estimate(base("staging"), 600)
}

test_zero_estimate_adds_nothing if {
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as with_estimate(with_budget(base("staging"), {"spent_usd": 2500}), 0)
}

# --- a missing or invalid estimate ----------------------------------------------------------

test_missing_estimate_denied_in_staging_and_production if {
	every env in ["staging", "production"] {
		ms := budget.deny with input as create_plan with data.iacp.context as base(env)
		ids(ms) == {"[iacp.budget.no_estimate]"}
		some m in ms
		contains(m, "no cost estimate is available")
		contains(m, "the change cannot be checked against it")
		count(budget.warn) == 0 with input as create_plan with data.iacp.context as base(env)
	}
}

test_missing_estimate_only_warns_in_development if {
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as base("development")
	ids(budget.warn) == {"[iacp.budget.no_estimate]"} with input as create_plan with data.iacp.context as base("development")
}

test_missing_estimate_denied_when_the_environment_is_unknown if {
	ctx := object.remove(base("staging"), ["environment"])
	ids(budget.deny) == {"[iacp.budget.no_estimate]"} with input as create_plan with data.iacp.context as ctx
}

test_null_estimate_counts_as_missing if {
	ctx := object.union(base("staging"), {"estimated_monthly_cost_usd": null})
	ids(budget.deny) == {"[iacp.budget.no_estimate]"} with input as create_plan with data.iacp.context as ctx
}

test_invalid_estimates_count_as_missing if {
	every bad in ["600", -5, true, [600], {"usd": 600}] {
		ms := budget.deny with input as create_plan with data.iacp.context as with_estimate(base("staging"), bad)
		ids(ms) == {"[iacp.budget.no_estimate]"}
		some m in ms
		contains(m, "the cost estimate is not a valid amount")
	}
}

test_override_waives_a_missing_estimate_in_staging_only if {
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as with_override(base("staging"), future)
	ids(budget.warn) == {"[iacp.budget.no_estimate]"} with input as create_plan with data.iacp.context as with_override(base("staging"), future)
	ms := budget.deny with input as create_plan with data.iacp.context as with_override(base("production"), future)
	ids(ms) == {"[iacp.budget.no_estimate]"}
}

test_soft_budget_only_warns_about_a_missing_estimate if {
	ctx := with_budget(base("production"), {"hard_block": false})
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
	ids(budget.warn) == {"[iacp.budget.no_estimate]"} with input as create_plan with data.iacp.context as ctx
}

test_missing_estimate_is_ignored_outside_the_gate if {
	# the validate job evaluates every rule before the cost job has run
	ctx := object.remove(base("production"), ["budget_gate"])
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
	count(budget.warn) == 0 with input as create_plan with data.iacp.context as ctx
	not budget.missing with input as create_plan with data.iacp.context as ctx
}

test_only_a_true_gate_flag_counts if {
	ctx := object.union(base("production"), {"budget_gate": "yes"})
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
}

# --- no budget, bad budget ----------------------------------------------------------------------

test_no_budget_means_no_findings if {
	ctx := object.union(object.remove(base("production"), ["budget"]), {"estimated_monthly_cost_usd": 99999})
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx
	count(budget.warn) == 0 with input as create_plan with data.iacp.context as ctx
	ctx2 := object.union(base("production"), {"budget": null, "estimated_monthly_cost_usd": 99999})
	count(budget.deny) == 0 with input as create_plan with data.iacp.context as ctx2
}

test_no_context_means_no_findings if {
	count(budget.deny) == 0 with input as create_plan
	count(budget.warn) == 0 with input as create_plan
}

test_budget_without_a_numeric_limit_is_denied if {
	ctx := replace_budget(base("staging"), {"limit_usd": "lots", "spent_usd": 0})
	ids(budget.deny) == {"[iacp.budget.invalid]"} with input as create_plan with data.iacp.context as ctx
	ctx2 := replace_budget(base("staging"), {"spent_usd": 0})
	ids(budget.deny) == {"[iacp.budget.invalid]"} with input as create_plan with data.iacp.context as ctx2
}

# --- the aggregate entrypoint ---------------------------------------------------------------------

test_budget_denies_reach_the_main_namespace if {
	ms := data.main.deny with input as create_plan with data.iacp.context as with_estimate(base("staging"), 600)
	"[iacp.budget.exceeded]" in ids(ms)
}
