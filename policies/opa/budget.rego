# METADATA
# title: Team budget
# description: |
#   Denies a change whose estimated monthly cost would push the team's month over its
#   hard budget, and warns near the limit. Inputs, all from data.iacp.context:
#
#     budget                      the month's budget from GET /v1/internal/budget-context
#                                 (limit_usd, spent_usd, actual_usd, hard_block, month,
#                                 optional single_change_cap_usd). No budget means nothing to
#                                 enforce.
#     current_committed_usd       the committed monthly cost of the team's other active
#                                 stacks in the budget's scope (this stack excluded), summed
#                                 by the API.
#     estimated_monthly_cost_usd  the stack's monthly cost, a plain number put there by
#                                 `tools.stack_pipeline budget-context` from the Infracost
#                                 output of the isolated cost job (docs/05, docs/07 6.7).
#     budget_gate                 true only in the evaluation that is the budget gate. Only
#                                 then does a missing estimate count (the validate job runs
#                                 these rules before any estimate exists).
#     budget_override             an admin override (granted_by, reason, expires_at,
#                                 max_delta_usd).
#
#   Already-used spend is the largest of spent_usd, actual_usd and current_committed_usd.
#   A plan that creates or updates nothing (destroy only, or no change) adds no cost, so it
#   always passes: a team over budget can still free budget.
#
#   Missing or invalid estimate with a budget set, in the gate:
#     development           warn (the estimate is informational there);
#     staging, production,  deny while the budget is a hard block; otherwise warn. Cost data
#     unknown environment   that cannot be read must never read as "free", and an override
#                           cannot waive it (its bound cannot be checked).
#
#   A single stack costing more per month than single_change_cap_usd is denied whatever the
#   headroom. There is no default cap (opt-in per budget): the estimate is the whole stack, not
#   the change, so a default would block updates of existing stacks above it.
#
#   The override waives a hard limit, or the cap, in development and staging only, and only
#   while the overrun it allows (projected - limit, or estimate - cap) is at most the
#   override's max_delta_usd. It never waives one
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

committed := num_or_zero(f.ctx_value("current_committed_usd", 0))

# used: what the month has already consumed.
used := max([spent, actual, committed])

# cap: the most one stack may cost per month, whatever the headroom. Undefined (no cap) unless the
# budget sets one: the estimate is the stack's whole cost, not the change, so a default would block
# every update of an existing stack above it.
cap := budget.single_change_cap_usd if is_number(budget.single_change_cap_usd)

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

# The override waives a limit only outside production, and only up to its bound: max_delta_usd caps
# how far past the limit (or the cap) it lets this change go, so several stacks under one override
# cannot overrun by more than that each. Without an estimate there is nothing to compare, so a
# missing estimate is never covered; a missing or unreadable bound is 0.
override_usable if {
	override_active
	env in {"development", "staging"}
	has_estimate
}

max_delta := num_or_zero(f.ctx.budget_override.max_delta_usd)

# covers_limit: the overrun this change causes (projected - limit) is within the bound.
covers_limit if {
	override_usable
	projected - limit <= max_delta
}

# covers_cap: how far this stack is over the cap is within the bound.
covers_cap if {
	override_usable
	estimate - cap <= max_delta
}

production_note := "; an admin override does not apply in production"

default limit_note := ""

limit_note := production_note if {
	override_active
	not env in {"development", "staging"}
}

limit_note := sprintf(
	"; the admin override covers an overrun of at most %s USD and this change overruns the limit by %s USD",
	[money(max_delta), money(projected - limit)],
) if {
	override_usable
	not covers_limit
}

default cap_note := ""

cap_note := production_note if {
	override_active
	not env in {"development", "staging"}
}

cap_note := sprintf(
	"; the admin override covers an excess of at most %s USD over the cap and this stack exceeds it by %s USD",
	[money(max_delta), money(estimate - cap)],
) if {
	override_usable
	not covers_cap
}

default missing_override_note := ""

missing_override_note := "; an admin override cannot waive a missing estimate" if override_active

blocked if {
	hard
	not covers_limit
}

# --- Exceeding the budget ----------------------------------------------------------------

deny contains m if {
	exceeded
	blocked
	m := f.msg(
		"iacp.budget.exceeded", address,
		sprintf(
			"estimated cost %s USD/month would raise %s's spend for %s to %s USD, over its %s USD budget (%s used already)%s",
			[money(estimate), team, month, money(projected), money(limit), money(used), limit_note],
		),
		sprintf(
			"reduce the stack's cost or destroy unused stacks, or ask a team admin to raise the budget (PUT /v1/teams/%s/budgets/%s) or, outside production, to grant an override that covers it",
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

waived_because := sprintf("an admin override granted by %s (covering an overrun of up to %s USD) is in effect", [f.ctx.budget_override.granted_by, money(max_delta)]) if {
	hard
	covers_limit
}

# --- The single-change cap ----------------------------------------------------------------

over_cap if {
	configured
	adds_cost
	estimate > cap
}

deny contains m if {
	over_cap
	not covers_cap
	m := f.msg(
		"iacp.budget.single_change_cap", address,
		sprintf(
			"estimated cost %s USD/month is above the %s USD single-change cap of %s's budget, whatever its headroom%s",
			[money(estimate), money(cap), team, cap_note],
		),
		sprintf(
			"reduce the stack's cost, or ask a team admin to raise single_change_cap_usd (PUT /v1/teams/%s/budgets/%s) or, outside production, to grant an override that covers it",
			[team, month],
		),
	)
}

warn contains m if {
	over_cap
	covers_cap
	m := f.msg(
		"iacp.budget.single_change_cap_waived", address,
		sprintf(
			"estimated cost %s USD/month is above the %s USD single-change cap of %s's budget; not blocked because an admin override granted by %s (covering an excess of up to %s USD) is in effect",
			[money(estimate), money(cap), team, f.ctx.budget_override.granted_by, money(max_delta)],
		),
		"review the spend with the team admin",
	)
}

# --- Near the limit -------------------------------------------------------------------------

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

# An override needs an estimate to be bounded, so it never makes a missing one acceptable: covers_limit
# is false without one, so blocked is just hard here.
no_estimate_denied if {
	missing
	env != "development"
	blocked
}

deny contains m if {
	no_estimate_denied
	m := f.msg(
		"iacp.budget.no_estimate", address,
		sprintf("%s's budget for %s is a hard block but %s, so the change cannot be checked against it%s", [team, month, no_estimate_reason, missing_override_note]),
		"re-run the pipeline once the Infracost job succeeds (an override cannot waive a missing estimate)",
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
