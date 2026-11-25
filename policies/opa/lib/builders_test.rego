# Test-only builders for small plan JSON fixtures.
package lib.testing

good_tags := {
	"iacp:team": "payments",
	"iacp:environment": "development",
	"iacp:stack": "orders",
	"iacp:managed-by": "iacp",
}

dev_ctx := {"environment": "development", "team": "payments", "stack": "orders"}

prod_ctx := {"environment": "production", "team": "payments", "stack": "orders"}

# rc builds a managed resource change being created at the root module.
rc(type, name, after) := {
	"address": sprintf("%s.%s", [type, name]),
	"mode": "managed",
	"type": type,
	"name": name,
	"change": {"actions": ["create"], "after": after, "after_unknown": {}},
}

# rc_in builds a managed resource change inside module.<mod>.
rc_in(mod, type, name, after) := object.union(rc(type, name, after), {
	"address": sprintf("module.%s.%s.%s", [mod, type, name]),
	"module_address": sprintf("module.%s", [mod]),
})

# with_actions overrides the planned actions of a resource change.
with_actions(r, actions) := object.union(r, {"change": object.union(r.change, {"actions": actions})})

# with_unknown marks attributes as known only after apply.
with_unknown(r, unknown) := object.union(r, {"change": object.union(r.change, {"after_unknown": unknown})})

plan(rcs) := {"resource_changes": rcs}

# cfg builds a configuration resource block with the given expressions.
cfg(type, name, expressions) := {
	"address": sprintf("%s.%s", [type, name]),
	"mode": "managed",
	"type": type,
	"name": name,
	"expressions": expressions,
}

# bucket_ref is the `bucket = aws_s3_bucket.<name>.id` expression.
bucket_ref(name) := {"bucket": {"references": [sprintf("aws_s3_bucket.%s.id", [name]), sprintf("aws_s3_bucket.%s", [name])]}}

# plan_with_config adds root-module configuration resources to a plan.
plan_with_config(rcs, resources) := {
	"resource_changes": rcs,
	"configuration": {"root_module": {"resources": resources}},
}

# rule_ids extracts the "[rule.id]" prefixes from a set of messages.
rule_ids(messages) := {id | some m in messages; id := regex.find_n(`^\[[a-z0-9._]+\]`, m, 1)[0]}
