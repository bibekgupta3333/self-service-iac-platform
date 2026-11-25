package lib.iacp_test

import data.lib.iacp as f
import data.lib.testing as t

test_changed_includes_create_and_update if {
	created := t.rc("aws_sqs_queue", "a", {})
	updated := t.with_actions(t.rc("aws_sqs_queue", "b", {}), ["update"])
	count(f.changed) == 2 with input as t.plan([created, updated])
}

test_changed_excludes_noop_and_delete if {
	noop := t.with_actions(t.rc("aws_sqs_queue", "a", {}), ["no-op"])
	deleted := t.with_actions(t.rc("aws_sqs_queue", "b", {}), ["delete"])
	count(f.changed) == 0 with input as t.plan([noop, deleted])
}

test_present_includes_noop_and_replace_but_not_delete if {
	noop := t.with_actions(t.rc("aws_s3_bucket", "a", {}), ["no-op"])
	replaced := t.with_actions(t.rc("aws_s3_bucket", "b", {}), ["delete", "create"])
	deleted := t.with_actions(t.rc("aws_s3_bucket", "c", {}), ["delete"])
	addrs := {r.address | some r in f.present_of_type("aws_s3_bucket")} with input as t.plan([noop, replaced, deleted])
	addrs == {"aws_s3_bucket.a", "aws_s3_bucket.b"}
}

test_same_module if {
	f.same_module(t.rc_in("x", "aws_s3_bucket", "a", {}), t.rc_in("x", "aws_kms_key", "k", {}))
	not f.same_module(t.rc_in("x", "aws_s3_bucket", "a", {}), t.rc("aws_kms_key", "k", {}))
}

test_environment_from_context if {
	f.environment_of(t.rc("aws_s3_bucket", "a", {})) == "staging" with data.iacp.context as {"environment": "staging"}
}

test_environment_from_tag if {
	f.environment_of(t.rc("aws_s3_bucket", "a", {"tags_all": t.good_tags})) == "development"
}

test_environment_unknown_fails_closed if {
	f.environment_of(t.rc("aws_s3_bucket", "a", {})) == "unknown"
}

test_as_array if {
	f.as_array("s3:GetObject") == ["s3:GetObject"]
	f.as_array(["a", "b"]) == ["a", "b"]
}

test_defaults_without_context if {
	f.allowed_regions == ["us-east-1", "us-west-2"]
	f.allowed_module_repos == ["github.com/bibekgupta3333/self-service-iac-platform"]
}

test_context_overrides_defaults if {
	f.allowed_regions == ["eu-west-1"] with data.iacp.context as {"allowed_regions": ["eu-west-1"]}
}

test_exempt_uses_wall_clock_without_now if {
	ctx := {"exceptions": [{"rule_id": "r", "resource_address": "a", "expires_at": "2200-01-01T00:00:00Z"}]}
	f.exempt("r", "a") with data.iacp.context as ctx
}

test_exempt_other_rule_not_covered if {
	ctx := {"exceptions": [{"rule_id": "r", "resource_address": "a", "expires_at": "2200-01-01T00:00:00Z"}]}
	not f.exempt("other", "a") with data.iacp.context as ctx
}

# --- Configuration lookup and bucket linking ------------------------------------

test_module_names_drop_instance_keys if {
	f.module_names(`module.a[0].module.b`) == ["a", "b"]
	f.module_names("") == []
}

test_config_resource_in_nested_module if {
	r := object.union(t.rc("aws_sqs_queue", "q", {}), {"module_address": `module.a["x"].module.b`})
	inner := {"resources": [t.cfg("aws_sqs_queue", "q", {"name": {"constant_value": "q"}})]}
	conf := {"configuration": {"root_module": {"module_calls": {"a": {"module": {"module_calls": {"b": {"module": inner}}}}}}}}
	f.config_expr(r, "name") == {"constant_value": "q"} with input as conf
}

test_config_expr_missing_is_empty if {
	f.config_expr(t.rc("aws_sqs_queue", "q", {}), "name") == {} with input as t.plan([])
}

test_linked_by_reference_with_unknown_bucket_name if {
	a := t.rc("aws_s3_bucket", "a", {"bucket": "probe-a"})
	pab := t.rc("aws_s3_bucket_public_access_block", "a", {})
	f.linked_to_bucket(pab, a) with input as t.plan_with_config([], [t.cfg(pab.type, "a", t.bucket_ref("a"))])
}

test_reference_to_other_bucket_not_linked if {
	b := t.rc("aws_s3_bucket", "b", {"bucket": "probe-b"})
	pab := t.rc("aws_s3_bucket_public_access_block", "a", {})
	not f.linked_to_bucket(pab, b) with input as t.plan_with_config([], [t.cfg(pab.type, "a", t.bucket_ref("a"))])
}

test_reference_with_instance_key_linked if {
	a := t.rc("aws_s3_bucket", "a", {})
	pab := t.rc("aws_s3_bucket_public_access_block", "a", {})
	expr := {"bucket": {"references": ["aws_s3_bucket.a[0].id"]}}
	f.linked_to_bucket(pab, a) with input as t.plan_with_config([], [t.cfg(pab.type, "a", expr)])
}

test_reference_prefix_of_other_name_not_linked if {
	a := t.rc("aws_s3_bucket", "a", {})
	pab := t.rc("aws_s3_bucket_public_access_block", "x", {})
	expr := {"bucket": {"references": ["aws_s3_bucket.ab.id"]}}
	not f.linked_to_bucket(pab, a) with input as t.plan_with_config([], [t.cfg(pab.type, "x", expr)])
}

test_unknown_child_bucket_without_reference_not_linked if {
	a := t.rc("aws_s3_bucket", "a", {"bucket": "probe-a"})
	pab := t.rc("aws_s3_bucket_public_access_block", "a", {})
	not f.linked_to_bucket(pab, a) with input as t.plan([])
}

test_data_resource_ref if {
	f.resource_ref({"mode": "data", "type": "aws_iam_policy_document", "name": "d"}) == "data.aws_iam_policy_document.d"
}

test_msg_format if {
	f.msg("x.y", "aws_s3_bucket.a", "bad", "do good") == "[x.y] aws_s3_bucket.a: bad. Fix: do good"
}
