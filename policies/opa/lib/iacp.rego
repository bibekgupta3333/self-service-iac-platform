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

# linked_to_bucket(child, bucket): child config is in the bucket's module and, when both
# names are known at plan time, targets the same bucket.
linked_to_bucket(child, bucket) if {
	same_module(child, bucket)
	not bucket_mismatch(child, bucket)
}

bucket_mismatch(child, bucket) if after(child).bucket != after(bucket).bucket

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
