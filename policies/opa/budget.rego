# METADATA
# title: Team budget
# description: |
#   Denies a change whose estimated monthly cost would push the team's month over its
#   hard budget, and warns near the limit. Inputs, all from data.iacp.context:
#
#     budget                      the month's budget from GET /v1/internal/budget-context
#                                 (limit_usd, spent_usd, actual_usd, hard_block, month).
#                                 No budget means nothing to enforce.
#     estimated_monthly_cost_usd  the stack's monthly cost, a plain number put there by
#                                 `tools.stack_pipeline budget-context` from the Infracost
#                                 output of the isolated cost job (docs/05, docs/07 6.7).
#     budget_gate                 true only in the evaluation that is the budget gate. Only
#                                 then does a missing estimate count (the validate job runs
#                                 these rules before any estimate exists).
#     budget_override             an admin override (granted_by, reason, expires_at).
#
#   Already-used spend is the larger of spent_usd (committed estimates) and actual_usd.
#   A plan that creates or updates nothing (destroy only, or no change) adds no cost, so it
#   always passes: a team over budget can still free budget.
#
#   Missing or invalid estimate with a budget set, in the gate:
#     development           warn (the estimate is informational there);
#     staging, production,  deny while the budget is a hard block, unless the override is
#     unknown environment   effective; otherwise warn. Cost data that cannot be read must
#                           never read as "free".
#
#   The override waives a hard limit in development and staging only. It never waives one
#   in production, and an unknown environment is treated as production. Budget rules are
#   not waivable through policy exceptions.
#
#   The warn threshold is 80 percent of the limit; context.budget.warn_pct overrides it.
package iacp.budget

import data.lib.iacp as f

default_warn_pct := 80

# --- Context ---------------------------------------------------------------------

budget := f.ctx_value("budget", null)

configured if {
	is_object(budget)
	is_number(budget.limit_usd)
}

# A budget object whose limit cannot be read must not read as "no budget".
deny contains m if {
	is_object(budget)
	not is_number(object.get(budget, "limit_usd", null))
	m := f.msg(
		"iacp.budget.invalid", f.ctx_value("stack_path", "this stack"),
		"the team budget in the policy context has no numeric limit_usd",
		"fix the budget with PUT /v1/teams/<team>/budgets/<month> and re-run the pipeline",
	)
}

gate if f.ctx_value("budget_gate", false) == true

env := f.ctx_value("environment", "unknown")

# A hard block unless hard_block is explicitly false (fail closed).
hard if object.get(budget, "hard_block", true) != false

limit := budget.limit_usd

num_or_zero(x) := x if is_number(x)

num_or_zero(x) := 0 if not is_number(x)

spent := num_or_zero(object.get(budget, "spent_usd", 0))

actual := num_or_zero(object.get(budget, "actual_usd", 0))

# used: what the month has already consumed.
used := max([spent, actual])

warn_pct := object.get(budget, "warn_pct", default_warn_pct)

address := f.ctx_value("stack_path", "this stack")

team := f.ctx_value("team", "the team")

month := object.get(budget, "month", "this month")

# --- Formatting ---------------------------------------------------------------------
#
# sprintf("%.2f") prints an integer number (600 in the JSON context) as "%!f(int64=600)",
# so amounts are formatted from whole cents and percentages from a whole number.

money(x) := sprintf("%d.%02d", [floor(cents / 100), cents - (floor(cents / 100) * 100)]) if {
	cents := round(x * 100)
}

percent(x) := sprintf("%d", [round(x)])

# --- The estimate ------------------------------------------------------------------

estimate := e if {
	e := f.ctx.estimated_monthly_cost_usd
	is_number(e)
	e >= 0
}

has_estimate if is_number(estimate)

projected := used + estimate

# A plan with no create or update (destroy only, or nothing to do) adds no cost. Without a
# resource_changes array nothing is known about the plan, so it is not assumed to be one.
frees_only if {
	is_array(input.resource_changes)
	count(f.changed) == 0
}

adds_cost if {
	has_estimate
	estimate > 0
	not frees_only
}

exceeded if {
	configured
	adds_cost
	projected > limit
}

# --- The override --------------------------------------------------------------------

override_active if {
	o := f.ctx.budget_override
	is_object(o)
	time.parse_rfc3339_ns(o.expires_at) > f.now_ns
}

override_effective if {
	override_active
	env in {"development", "staging"}
}

override_ignored_note := "; an admin override does not apply in production" if {
	override_active
	not env in {"development", "staging"}
}

override_ignored_note := "" if not override_active

override_ignored_note := "" if {
	override_active
	env in {"development", "staging"}
}

blocked if {
	hard
	not override_effective
}

# --- Exceeding the budget ----------------------------------------------------------------

deny contains m if {
	exceeded
	blocked
	m := f.msg(
		"iacp.budget.exceeded", address,
		sprintf(
			"estimated cost %s USD/month would raise %s's spend for %s to %s USD, over its %s USD budget (%s used already)%s",
			[money(estimate), team, month, money(projected), money(limit), money(used), override_ignored_note],
		),
		sprintf(
			"reduce the stack's cost or destroy unused stacks, or ask a team admin to raise the budget (PUT /v1/teams/%s/budgets/%s) or, outside production, to grant an override",
			[team, month],
		),
	)
}

warn contains m if {
	exceeded
	not blocked
	m := f.msg(
		"iacp.budget.exceeded_waived", address,
		sprintf(
			"estimated cost %s USD/month would raise %s's spend for %s to %s USD, over its %s USD budget; not blocked because %s",
			[money(estimate), team, month, money(projected), money(limit), waived_because],
		),
		"review the spend with the team admin",
	)
}

waived_because := "the budget is advisory (hard_block is off)" if not hard

waived_because := sprintf("an admin override granted by %s is in effect", [f.ctx.budget_override.granted_by]) if {
	hard
	override_effective
}

warn contains m if {
	configured
	adds_cost
	not exceeded
	projected >= (limit * warn_pct) / 100
	m := f.msg(
		"iacp.budget.near_limit", address,
		sprintf(
			"estimated cost %s USD/month brings %s's spend for %s to %s USD, %s%% of its %s USD budget",
			[money(estimate), team, month, money(projected), percent((100 * projected) / limit), money(limit)],
		),
		sprintf("check the remaining budget before merging (warning from %s%% of the limit)", [percent(warn_pct)]),
	)
}

# --- A missing or invalid estimate ---------------------------------------------------

missing if {
	configured
	gate
	not has_estimate
	not frees_only
}

no_estimate_reason := "the cost estimate is not a valid amount" if {
	object.get(f.ctx, "estimated_monthly_cost_usd", null) != null
}

no_estimate_reason := "no cost estimate is available (Infracost did not run or reported no total)" if {
	object.get(f.ctx, "estimated_monthly_cost_usd", null) == null
}

no_estimate_denied if {
	missing
	env != "development"
	blocked
}

deny contains m if {
	no_estimate_denied
	m := f.msg(
		"iacp.budget.no_estimate", address,
		sprintf("%s's budget for %s is a hard block but %s, so the change cannot be checked against it%s", [team, month, no_estimate_reason, override_ignored_note]),
		"re-run the pipeline once the Infracost job succeeds, or ask a team admin to grant an override (not available in production)",
	)
}

warn contains m if {
	missing
	not no_estimate_denied
	m := f.msg(
		"iacp.budget.no_estimate", address,
		sprintf("%s, so the change is not checked against %s's budget for %s", [no_estimate_reason, team, month]),
		"configure Infracost (INFRACOST_API_KEY) so budgets can be enforced",
	)
}
