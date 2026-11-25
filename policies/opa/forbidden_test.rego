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
	count(forbidden.deny) == 0 with input as t.plan([bucket(true, "development")])
}

test_force_destroy_denied_in_staging if {
	some m in forbidden.deny with input as t.plan([bucket(true, "staging")])
	startswith(m, `[iacp.forbidden.force_destroy] aws_s3_bucket.b: force_destroy = true is not allowed in environment "staging"`)
}

test_force_destroy_context_wins_over_tag if {
	count(forbidden.deny) == 1 with input as t.plan([bucket(true, "development")]) with data.iacp.context as t.prod_ctx
}

test_force_destroy_false_allowed_in_production if {
	count(forbidden.deny) == 0 with input as t.plan([bucket(false, "production")])
}
