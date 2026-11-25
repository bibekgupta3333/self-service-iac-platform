# METADATA
# title: Shared helpers for iacp policies
# description: |
#   Small, pure helpers over Terraform plan JSON (`terraform show -json tfplan`).
#   Every policy package imports this library as `data.lib.iacp`.
#   Optional context (environment, team, allowed regions, exceptions) is read
#   from `data.iacp.context` (conftest `--data context.json`); safe defaults
#   apply when it is absent.
package lib.iacp

# --- Context ---------------------------------------------------------------

default_allowed_regions := ["us-east-1", "us-west-2"]

default_allowed_module_repos := ["github.com/bibekgupta3333/self-service-iac-platform"]

default ctx := {}

ctx := data.iacp.context

# ctx_value returns ctx[key], or fallback when the context does not define it.
ctx_value(key, fallback) := object.get(ctx, key, fallback)

allowed_regions := ctx_value("allowed_regions", default_allowed_regions)

allowed_module_repos := ctx_value("allowed_module_repos", default_allowed_module_repos)

# --- Resource selection ------------------------------------------------------

# Managed resources being created or updated (an after-state exists).
changed contains rc if {
	some rc in input.resource_changes
	rc.mode == "managed"
	creates_or_updates(rc)
}

creates_or_updates(rc) if {
	some action in rc.change.actions
	action in {"create", "update"}
}

changed_of_type(t) := {rc | some rc in changed; rc.type == t}

# Managed resources that will exist after apply (created, updated, replaced or unchanged).
present contains rc if {
	some rc in input.resource_changes
	rc.mode == "managed"
	rc.change.actions != ["delete"]
}

present_of_type(t) := {rc | some rc in present; rc.type == t}

after(rc) := object.get(rc.change, "after", {})

# after_unknown(rc, key) is true when the attribute is computed at apply time.
after_unknown(rc, key) if object.get(object.get(rc.change, "after_unknown", {}), key, false) == true

# module_of(rc) is the module path of a resource ("" for the root module).
module_of(rc) := object.get(rc, "module_address", "")

same_module(a, b) if module_of(a) == module_of(b)

# --- Configuration lookup -------------------------------------------------------

# module_names(addr): ["a", "b"] for "module.a[0].module.b" (instance keys dropped).
module_names(addr) := [m[1] | some m in regex.find_all_string_submatch_n(`module\.([^.\[]+)`, addr, -1)]

# config_module(rc): the configuration block of the module that declares rc.
config_module(rc) := object.get(
	input,
	array.concat(
		["configuration", "root_module"],
		[step | some name in module_names(module_of(rc)); some step in ["module_calls", name, "module"]],
	),
	{},
)

# config_resources(rc): the resource block(s) in configuration that rc was planned from.
config_resources(rc) := [r |
	some r in object.get(config_module(rc), "resources", [])
	r.mode == rc.mode
	r.type == rc.type
	r.name == rc.name
]

# config_expr(rc, key): the configuration expression for attribute key ({} when absent).
config_expr(rc, key) := object.get(array.concat(config_resources(rc), [{}])[0], ["expressions", key], {})

# refers_to(rc, key, target): rc's `key` expression references the target resource
# (same module, matched by type and name, any attribute or instance key).
refers_to(rc, key, target) if {
	same_module(rc, target)
	prefix := resource_ref(target)
	some ref in config_expr(rc, key).references
	ref_matches(ref, prefix)
}

resource_ref(rc) := concat(".", [rc.type, rc.name]) if rc.mode == "managed"

resource_ref(rc) := concat(".", ["data", rc.type, rc.name]) if rc.mode == "data"

ref_matches(ref, prefix) if ref == prefix

ref_matches(ref, prefix) if startswith(ref, concat("", [prefix, "."]))

ref_matches(ref, prefix) if startswith(ref, concat("", [prefix, "["]))

# linked_to_bucket(child, bucket): child (PAB, SSE config, policy, ACL) targets the bucket,
# shown by a configuration reference or by equal bucket names known at plan time.
# Without either, the child is not linked (fail closed).
linked_to_bucket(child, bucket) if refers_to(child, "bucket", bucket)

linked_to_bucket(child, bucket) if {
	same_module(child, bucket)
	name := after(child).bucket
	is_string(name)
	name == after(bucket).bucket
}

# --- Policy documents --------------------------------------------------------------

# policy_documents(rc, attr): the IAM JSON documents held in rc's attribute attr, from
#   1. the planned value, when known;
#   2. a literal string in configuration (constant_value);
#   3. a referenced data.aws_iam_policy_document, whose statements keep their known
#      parts (actions, principals) even when resources are computed.
policy_documents(rc, attr) := (known_documents(rc, attr) | constant_documents(rc, attr)) | data_documents(rc, attr)

known_documents(rc, attr) := {json.unmarshal(doc) |
	doc := after(rc)[attr]
	is_string(doc)
}

known_string(rc, attr) if is_string(after(rc)[attr])

constant_documents(rc, attr) := {json.unmarshal(doc) |
	not known_string(rc, attr)
	doc := config_expr(rc, attr).constant_value
	is_string(doc)
}

data_documents(rc, attr) := {{"Statement": [normalize_statement(s) | some s in after(d).statement]} |
	not known_string(rc, attr)
	some d in input.resource_changes
	d.mode == "data"
	d.type == "aws_iam_policy_document"
	refers_to(rc, attr, d)
}

# policy_unknown(rc, attr): the document is computed at apply time and cannot be read
# from the plan at all (for example jsonencode() with a computed Resource).
policy_unknown(rc, attr) if {
	after_unknown(rc, attr)
	count(policy_documents(rc, attr)) == 0
}

# normalize_statement maps an aws_iam_policy_document statement block to IAM JSON
# keys. Empty keys are dropped; unknown list items stay null.
normalize_statement(s) := {k: v |
	some k, v in {
		"Sid": object.get(s, "sid", null),
		"Effect": effect(object.get(s, "effect", null)),
		"Action": object.get(s, "actions", null),
		"NotAction": object.get(s, "not_actions", null),
		"Resource": object.get(s, "resources", null),
		"NotResource": object.get(s, "not_resources", null),
		"Principal": principal(object.get(s, "principals", null)),
		"NotPrincipal": principal(object.get(s, "not_principals", null)),
		"Condition": condition(object.get(s, "condition", null)),
	}
	non_empty(v)
}

effect(null) := "Allow"

effect(e) := e if e != null

principal(ps) := "*" if {
	some p in ps
	p.type == "*"
}

principal(null) := null

principal(ps) := {p.type: p.identifiers | some p in ps} if {
	is_array(ps)
	every p in ps { p.type != "*" }
}

condition(cs) := {op: {c.variable: c.values | some c in cs; c.test == op} |
	some c0 in cs
	op := c0.test
} if is_array(cs)

condition(null) := null

non_empty(v) if {
	v != null
	v != []
	v != {}
}

# --- Environment -------------------------------------------------------------

# environment_of(rc): the context environment, else the resource's
# iacp:environment tag, else "unknown" (treated as non-development: fail closed).
environment_of(_) := ctx.environment

environment_of(rc) := env if {
	not ctx.environment
	env := tags_of(rc)["iacp:environment"]
}

environment_of(rc) := "unknown" if {
	not ctx.environment
	not tags_of(rc)["iacp:environment"]
}

# --- Tags ----------------------------------------------------------------------

# tags_of(rc): tags_all (includes provider default_tags) merged over tags.
tags_of(rc) := object.union(
	object_or_empty(object.get(after(rc), "tags", {})),
	object_or_empty(object.get(after(rc), "tags_all", {})),
)

taggable(rc) if "tags_all" in object.keys(after(rc))

taggable(rc) if "tags" in object.keys(after(rc))

taggable(rc) if after_unknown(rc, "tags_all")

# --- Generic value helpers -----------------------------------------------------

# as_array(x) normalises IAM-style "string or list" values to a list.
as_array(x) := x if is_array(x)

as_array(x) := [x] if not is_array(x)

# object_or_empty(x) maps null (or any non-object) to {}.
object_or_empty(x) := x if is_object(x)

object_or_empty(x) := {} if not is_object(x)

# --- Exceptions ----------------------------------------------------------------

# exempt(rule_id, address) is true when an unexpired exception covers the
# rule for this resource (or for every resource when resource_address is null).
exempt(rule_id, address) if {
	some e in ctx_value("exceptions", [])
	e.rule_id == rule_id
	e.resource_address in {address, null}
	time.parse_rfc3339_ns(e.expires_at) > now_ns
}

now_ns := time.parse_rfc3339_ns(ctx.now)

now_ns := time.now_ns() if not ctx.now

# --- Messages --------------------------------------------------------------------

# msg builds "[rule_id] address: problem. Fix: remedy".
msg(rule_id, address, problem, fix) := sprintf("[%s] %s: %s. Fix: %s", [rule_id, address, problem, fix])
