package iacp.tags_test

import data.iacp.tags
import data.lib.testing as t

queue(tag_obj) := t.rc("aws_sqs_queue", "q", {"name": "q", "tags_all": tag_obj})

test_fully_tagged_resource_allowed if {
	count(tags.deny) == 0 with input as t.plan([queue(t.good_tags)])
}

test_missing_tags_denied if {
	some m in tags.deny with input as t.plan([queue({"iacp:team": "payments"})])
	contains(m, "[iacp.tags.required] aws_sqs_queue.q")
	contains(m, `"iacp:environment"`)
	contains(m, "Fix:")
}

test_empty_tag_value_denied if {
	bad := object.union(t.good_tags, {"iacp:stack": ""})
	some m in tags.deny with input as t.plan([queue(bad)])
	contains(m, `missing or empty tags ["iacp:stack"]`)
}

test_null_tags_denied if {
	r := t.rc("aws_sqs_queue", "q", {"name": "q", "tags": null, "tags_all": null})
	count(tags.deny) == 1 with input as t.plan([r])
}

test_tags_without_tags_all_used if {
	r := t.rc("aws_sqs_queue", "q", {"tags": t.good_tags})
	count(tags.deny) == 0 with input as t.plan([r])
}

test_untaggable_resource_ignored if {
	r := t.rc("aws_s3_bucket_versioning", "v", {"bucket": "b"})
	count(tags.deny) == 0 with input as t.plan([r])
}

test_unknown_tags_all_is_taggable if {
	r := t.with_unknown(t.rc("aws_sqs_queue", "q", {}), {"tags_all": true})
	count(tags.deny) == 1 with input as t.plan([r])
}

test_deleted_resource_ignored if {
	r := t.with_actions(queue({}), ["delete"])
	count(tags.deny) == 0 with input as t.plan([r])
}

test_data_source_ignored if {
	r := object.union(queue({}), {"mode": "data"})
	count(tags.deny) == 0 with input as t.plan([r])
}

test_exception_suppresses_required if {
	ctx := {"now": "2026-01-01T00:00:00Z", "exceptions": [{
		"rule_id": "iacp.tags.required",
		"resource_address": "aws_sqs_queue.q",
		"expires_at": "2027-01-01T00:00:00Z",
	}]}
	count(tags.deny) == 0 with input as t.plan([queue({})]) with data.iacp.context as ctx
}

test_expired_exception_ignored if {
	ctx := {"now": "2026-01-01T00:00:00Z", "exceptions": [{
		"rule_id": "iacp.tags.required",
		"resource_address": null,
		"expires_at": "2025-01-01T00:00:00Z",
	}]}
	count(tags.deny) == 1 with input as t.plan([queue({})]) with data.iacp.context as ctx
}

test_team_matching_context_allowed if {
	count(tags.deny) == 0 with input as t.plan([queue(t.good_tags)]) with data.iacp.context as t.dev_ctx
}

test_team_mismatch_denied if {
	bad := object.union(t.good_tags, {"iacp:team": "search"})
	some m in tags.deny with input as t.plan([queue(bad)]) with data.iacp.context as t.dev_ctx
	contains(m, `[iacp.tags.mismatch] aws_sqs_queue.q: tag iacp:team="search"`)
}

test_environment_mismatch_denied if {
	count(tags.deny) == 1 with input as t.plan([queue(t.good_tags)]) with data.iacp.context as t.prod_ctx
}
