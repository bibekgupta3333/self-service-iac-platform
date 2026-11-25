package iacp.encryption_test

import data.iacp.encryption as enc
import data.lib.testing as t

bucket := t.rc_in("data", "aws_s3_bucket", "this", {"bucket": "orders-data"})

sse(algorithm) := t.rc_in("data", "aws_s3_bucket_server_side_encryption_configuration", "this", {
	"bucket": "orders-data",
	"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": algorithm}]}],
})

rule_ids(denies) := {id | some m in denies; id := regex.find_n(`^\[[a-z0-9._]+\]`, m, 1)[0]}

# --- S3 ------------------------------------------------------------------------

test_s3_with_kms_sse_allowed if {
	count(enc.deny) == 0 with input as t.plan([bucket, sse("aws:kms")])
}

test_s3_with_dsse_allowed if {
	count(enc.deny) == 0 with input as t.plan([bucket, sse("aws:kms:dsse")])
}

test_s3_with_existing_unchanged_sse_allowed if {
	count(enc.deny) == 0 with input as t.plan([bucket, t.with_actions(sse("aws:kms"), ["no-op"])])
}

test_s3_with_aes256_denied if {
	some m in enc.deny with input as t.plan([bucket, sse("AES256")])
	startswith(m, "[iacp.encryption.s3_kms] module.data.aws_s3_bucket.this: ")
	contains(m, "Fix:")
}

test_s3_without_sse_denied if {
	rule_ids(enc.deny) == {"[iacp.encryption.s3_kms]"} with input as t.plan([bucket])
}

test_s3_sse_in_other_module_denied if {
	other := t.rc_in("other", "aws_s3_bucket_server_side_encryption_configuration", "this", sse("aws:kms").change.after)
	count(enc.deny) == 1 with input as t.plan([bucket, other])
}

test_s3_sse_for_other_bucket_denied if {
	other := t.rc_in("data", "aws_s3_bucket_server_side_encryption_configuration", "logs", object.union(
		sse("aws:kms").change.after,
		{"bucket": "orders-logs"},
	))
	count(enc.deny) == 1 with input as t.plan([bucket, other])
}

test_s3_sse_being_deleted_denied if {
	count(enc.deny) == 1 with input as t.plan([bucket, t.with_actions(sse("aws:kms"), ["delete"])])
}

# Two buckets in one module; only `a` has an SSE config, whose `bucket` is unknown
# at plan time. The config must not count for `b`.
test_s3_two_bucket_probe_only_unlinked_bucket_denied if {
	a := t.rc("aws_s3_bucket", "a", {"bucket": "probe-a"})
	b := t.rc("aws_s3_bucket", "b", {"bucket": "probe-b"})
	sse_a := t.rc("aws_s3_bucket_server_side_encryption_configuration", "a", {"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": "aws:kms", "kms_master_key_id": "arn:aws:kms:us-east-1:111111111111:key/k"}]}]})
	inp := t.plan_with_config([a, b, sse_a], [t.cfg(sse_a.type, "a", t.bucket_ref("a"))])
	denied := {m | some m in enc.deny; contains(m, "iacp.encryption.s3_kms")} with input as inp
	count(denied) == 1
	some m in denied
	contains(m, "aws_s3_bucket.b:")
}

test_s3_inline_kms_allowed if {
	b := t.rc("aws_s3_bucket", "legacy", {"server_side_encryption_configuration": [{"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": "aws:kms"}]}]}]})
	count(enc.deny) == 0 with input as t.plan([b])
}

# --- SQS -----------------------------------------------------------------------

queue(after) := t.rc("aws_sqs_queue", "q", after)

test_sqs_with_kms_allowed_in_production if {
	q := queue({"kms_master_key_id": "alias/iacp-orders"})
	count(enc.deny) == 0 with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_with_unknown_kms_allowed if {
	q := t.with_unknown(queue({}), {"kms_master_key_id": true})
	count(enc.deny) == 0 with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_managed_sse_allowed_in_development if {
	q := queue({"sqs_managed_sse_enabled": true, "kms_master_key_id": ""})
	count(enc.deny) == 0 with input as t.plan([q]) with data.iacp.context as t.dev_ctx
}

test_sqs_managed_sse_denied_in_production if {
	q := queue({"sqs_managed_sse_enabled": true})
	rule_ids(enc.deny) == {"[iacp.encryption.sqs_kms]"} with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_managed_sse_denied_without_environment if {
	q := queue({"sqs_managed_sse_enabled": true})
	some m in enc.deny with input as t.plan([q])
	contains(m, `environment "unknown"`)
}

test_sqs_unencrypted_denied if {
	q := queue({"sqs_managed_sse_enabled": false})
	rule_ids(enc.deny) == {"[iacp.encryption.sqs_sse]"} with input as t.plan([q]) with data.iacp.context as t.dev_ctx
}

# --- KMS -----------------------------------------------------------------------

test_kms_rotation_enabled_allowed if {
	count(enc.deny) == 0 with input as t.plan([t.rc("aws_kms_key", "k", {"enable_key_rotation": true})])
}

test_kms_rotation_disabled_denied if {
	some m in enc.deny with input as t.plan([t.rc("aws_kms_key", "k", {"enable_key_rotation": false})])
	startswith(m, "[iacp.encryption.kms_rotation] aws_kms_key.k")
}
