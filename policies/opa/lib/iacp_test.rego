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

test_msg_format if {
	f.msg("x.y", "aws_s3_bucket.a", "bad", "do good") == "[x.y] aws_s3_bucket.a: bad. Fix: do good"
}
