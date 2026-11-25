package main_test

import data.lib.testing as t
import data.main

test_aggregates_denies_from_all_packages if {
	q := t.rc("aws_sqs_queue", "q", {"sqs_managed_sse_enabled": false, "tags_all": {}})
	ids := {regex.find_n(`^\[[a-z0-9._]+\]`, m, 1)[0] | some m in main.deny} with input as t.plan([q])
	ids == {"[iacp.tags.required]", "[iacp.encryption.sqs_sse]", "[iacp.encryption.sqs_kms]"}
}

test_aggregates_warnings if {
	inp := {"configuration": {"provider_config": {"aws": {"name": "aws", "expressions": {"region": {"references": ["var.region"]}}}}}}
	count(main.warn) == 1 with input as inp
}

test_clean_plan_has_no_denies if {
	q := t.rc("aws_sqs_queue", "q", {"kms_master_key_id": "alias/k", "tags_all": t.good_tags})
	count(main.deny) == 0 with input as t.plan([q])
}
