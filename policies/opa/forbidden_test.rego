package iacp.forbidden_test

import data.iacp.forbidden
import data.lib.testing as t

repo := "git::https://github.com/bibekgupta3333/self-service-iac-platform.git"

calls(source) := {"configuration": {"root_module": {"module_calls": {"store": {"source": source}}}}}

# --- Module source ---------------------------------------------------------------

test_pinned_platform_module_allowed if {
	count(forbidden.deny) == 0 with input as calls(concat("", [repo, "//modules/s3?ref=modules/s3/v1.2.0"]))
}

test_ref_main_denied if {
	some m in forbidden.deny with input as calls(concat("", [repo, "//modules/s3?ref=main"]))
	startswith(m, "[iacp.forbidden.module_source] module.store: source ")
	contains(m, "never a branch such as main")
}

test_missing_ref_denied if {
	count(forbidden.deny) == 1 with input as calls(concat("", [repo, "//modules/s3"]))
}

test_partial_version_denied if {
	count(forbidden.deny) == 1 with input as calls(concat("", [repo, "//modules/s3?ref=modules/s3/v1.2"]))
}

test_ref_for_other_module_denied if {
	count(forbidden.deny) == 1 with input as calls(concat("", [repo, "//modules/s3?ref=modules/sqs/v1.2.0"]))
}

test_foreign_repo_denied if {
	src := "git::https://github.com/evil/self-service-iac-platform.git//modules/s3?ref=modules/s3/v1.2.0"
	count(forbidden.deny) == 1 with input as calls(src)
}

test_registry_module_denied if {
	count(forbidden.deny) == 1 with input as calls("terraform-aws-modules/s3-bucket/aws")
}

test_local_path_denied if {
	count(forbidden.deny) == 1 with input as calls("../../modules/s3")
}

test_context_repo_allow_list_used if {
	src := "git::https://github.com/acme/self-service-iac-platform.git//modules/s3?ref=modules/s3/v1.2.0"
	count(forbidden.deny) == 0 with input as calls(src)
		with data.iacp.context as {"allowed_module_repos": ["github.com/acme/self-service-iac-platform"]}
}

# --- force_destroy ---------------------------------------------------------------

bucket(force, env) := t.rc("aws_s3_bucket", "b", {"force_destroy": force, "tags_all": {"iacp:environment": env}})

test_force_destroy_allowed_in_development if {
	count(forbidden.deny) == 0 with input as t.plan([bucket(true, "development")]) with data.iacp.context as t.dev_ctx
}

test_force_destroy_denied_in_staging if {
	some m in forbidden.deny with input as t.plan([bucket(true, "staging")]) with data.iacp.context as {"environment": "staging"}
	startswith(m, `[iacp.forbidden.force_destroy] aws_s3_bucket.b: force_destroy = true is not allowed in environment "staging"`)
}

# A development tag must not unlock force_destroy when no context says so.
test_force_destroy_development_tag_without_context_denied if {
	some m in forbidden.deny with input as t.plan([bucket(true, "development")])
	contains(m, `environment "unknown"`)
}

test_force_destroy_exception_ignored if {
	ctx := {"environment": "staging", "now": "2026-01-01T00:00:00Z", "exceptions": [{
		"rule_id": "iacp.forbidden.force_destroy",
		"resource_address": "aws_s3_bucket.b",
		"expires_at": "2026-06-01T00:00:00Z",
	}]}
	count(forbidden.deny) == 1 with input as t.plan([bucket(true, "staging")]) with data.iacp.context as ctx
}

test_force_destroy_context_wins_over_tag if {
	count(forbidden.deny) == 1 with input as t.plan([bucket(true, "development")]) with data.iacp.context as t.prod_ctx
}

test_force_destroy_false_allowed_in_production if {
	count(forbidden.deny) == 0 with input as t.plan([bucket(false, "production")])
}

# --- Data sources and provisioners ---------------------------------------------------

block(mode, type, name, extra) := object.union(
	{"address": concat(".", array.concat(["data" | mode == "data"], [type, name])), "mode": mode, "type": type, "name": name},
	extra,
)

root_config(resources) := {"configuration": {"root_module": {"resources": resources}}}

test_external_data_source_denied if {
	some m in forbidden.deny with input as root_config([block("data", "external", "x", {})])
	startswith(m, `[iacp.forbidden.data_source] data.external.x: data "external" can run programs`)
}

test_http_data_source_in_nested_module_denied if {
	inner := {"resources": [block("data", "http", "h", {})]}
	inp := {"configuration": {"root_module": {"module_calls": {"a": {"module": {"module_calls": {"b": {"module": inner}}}}}}}}
	some m in forbidden.deny with input as inp
	startswith(m, "[iacp.forbidden.data_source] module.a.module.b.data.http.h:")
}

test_aws_data_source_allowed if {
	count(forbidden.deny) == 0 with input as root_config([block("data", "aws_caller_identity", "me", {})])
}

test_provisioner_denied if {
	r := block("managed", "terraform_data", "run", {"provisioners": [{"type": "local-exec", "expressions": {}}]})
	some m in forbidden.deny with input as root_config([r])
	startswith(m, "[iacp.forbidden.provisioner] terraform_data.run: local-exec provisioner runs arbitrary commands")
}

test_resource_without_provisioner_allowed if {
	count(forbidden.deny) == 0 with input as root_config([block("managed", "terraform_data", "marker", {})])
}

# "resources" keys inside expressions (e.g. a policy document) are not blocks.
test_expression_named_resources_ignored if {
	r := block("managed", "aws_iam_policy", "p", {"expressions": {"resources": [block("data", "external", "x", {})]}})
	count(forbidden.deny) == 0 with input as root_config([r])
}
