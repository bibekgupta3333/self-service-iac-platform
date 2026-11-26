# METADATA
# title: Encryption at rest
# description: |
#   S3 buckets must use SSE-KMS, and SQS queues and SNS topics must be
#   encrypted; outside development all three need a customer-managed key (not
#   alias/aws/*), and so do DynamoDB tables. KMS keys must rotate.
package iacp.encryption

import data.lib.iacp as f

kms_algorithms := {"aws:kms", "aws:kms:dsse"}

# customer_managed_key(key): a key ID, ARN or alias that is not an AWS-managed
# alias such as alias/aws/s3 or alias/aws/sqs.
customer_managed_key(key) if {
	is_string(key)
	key != ""
	not regex.match(`(^|:)alias/aws/`, key)
}

# cmk_or_unknown(key, unknown): a customer-managed key, or one computed at apply
# time (a key created in the same plan).
cmk_or_unknown(_, true)

cmk_or_unknown(key, false) if customer_managed_key(key)

# --- S3 ----------------------------------------------------------------------

# uses_kms(rules) is true when any SSE rule defaults to a KMS algorithm.
uses_kms(rules) if {
	some rule in rules
	some d in rule.apply_server_side_encryption_by_default
	d.sse_algorithm in kms_algorithms
}

bucket_has_kms(b) if {
	some sse in f.present_of_type("aws_s3_bucket_server_side_encryption_configuration")
	f.linked_to_bucket(sse, b)
	uses_kms(f.after(sse).rule)
}

# Legacy inline configuration on aws_s3_bucket.
bucket_has_kms(b) if {
	some cfg in f.after(b).server_side_encryption_configuration
	uses_kms(cfg.rule)
}

# bucket_has_cmk(b): a linked KMS default names a customer-managed key.
bucket_has_cmk(b) if {
	some sse in f.present_of_type("aws_s3_bucket_server_side_encryption_configuration")
	f.linked_to_bucket(sse, b)
	some i, j
	d := f.after(sse).rule[i].apply_server_side_encryption_by_default[j]
	d.sse_algorithm in kms_algorithms
	path := ["rule", i, "apply_server_side_encryption_by_default", j, "kms_master_key_id"]
	cmk_or_unknown(object.get(d, "kms_master_key_id", null), unknown_at(sse, path))
}

bucket_has_cmk(b) if {
	some k, i, j
	d := f.after(b).server_side_encryption_configuration[k].rule[i].apply_server_side_encryption_by_default[j]
	d.sse_algorithm in kms_algorithms
	path := ["server_side_encryption_configuration", k, "rule", i, "apply_server_side_encryption_by_default", j, "kms_master_key_id"]
	cmk_or_unknown(object.get(d, "kms_master_key_id", null), unknown_at(b, path))
}

unknown_at(rc, path) := object.get(object.get(rc.change, "after_unknown", {}), path, false) == true

deny contains m if {
	# Every bucket that will exist, so disabling a control on an unchanged bucket is caught.
	some b in f.present_of_type("aws_s3_bucket")
	not bucket_has_kms(b)
	not f.exempt("iacp.encryption.s3_kms", b.address)
	m := f.msg(
		"iacp.encryption.s3_kms", b.address,
		"bucket is not encrypted with SSE-KMS",
		`add aws_s3_bucket_server_side_encryption_configuration with sse_algorithm = "aws:kms" and a customer-managed key`,
	)
}

deny contains m if {
	some b in f.present_of_type("aws_s3_bucket")
	f.environment_of(b) != "development"
	bucket_has_kms(b)
	not bucket_has_cmk(b)
	not f.exempt("iacp.encryption.s3_cmk", b.address)
	m := f.msg(
		"iacp.encryption.s3_cmk", b.address,
		sprintf("bucket in environment %q uses the AWS-managed aws/s3 key", [f.environment_of(b)]),
		"set kms_master_key_id to a customer-managed KMS key",
	)
}

# --- SQS ---------------------------------------------------------------------

has_kms_key(q) if {
	key := f.after(q).kms_master_key_id
	is_string(key)
	key != ""
}

has_kms_key(q) if f.after_unknown(q, "kms_master_key_id")

has_cmk(q) if cmk_or_unknown(object.get(f.after(q), "kms_master_key_id", null), unknown_at(q, ["kms_master_key_id"]))

encrypted_queue(q) if has_kms_key(q)

encrypted_queue(q) if f.after(q).sqs_managed_sse_enabled == true

deny contains m if {
	some q in f.changed_of_type("aws_sqs_queue")
	not encrypted_queue(q)
	not f.exempt("iacp.encryption.sqs_sse", q.address)
	m := f.msg(
		"iacp.encryption.sqs_sse", q.address,
		"queue has no server-side encryption",
		"set kms_master_key_id to a customer-managed key (or sqs_managed_sse_enabled = true in development)",
	)
}

deny contains m if {
	some q in f.changed_of_type("aws_sqs_queue")
	f.environment_of(q) != "development"
	not has_cmk(q)
	not f.exempt("iacp.encryption.sqs_kms", q.address)
	m := f.msg(
		"iacp.encryption.sqs_kms", q.address,
		sprintf("queue in environment %q is not encrypted with a customer-managed KMS key", [f.environment_of(q)]),
		"set kms_master_key_id to a customer-managed key; SQS-managed SSE and alias/aws/sqs are only allowed in development",
	)
}

# --- SNS ---------------------------------------------------------------------

topic_has_key(t) if {
	key := f.after(t).kms_master_key_id
	is_string(key)
	key != ""
}

topic_has_key(t) if f.after_unknown(t, "kms_master_key_id")

topic_has_cmk(t) if cmk_or_unknown(object.get(f.after(t), "kms_master_key_id", null), unknown_at(t, ["kms_master_key_id"]))

deny contains m if {
	some t in f.changed_of_type("aws_sns_topic")
	not topic_has_key(t)
	not f.exempt("iacp.encryption.sns_sse", t.address)
	m := f.msg(
		"iacp.encryption.sns_sse", t.address,
		"topic has no server-side encryption",
		"set kms_master_key_id to a customer-managed key (alias/aws/sns is only allowed in development)",
	)
}

deny contains m if {
	some t in f.changed_of_type("aws_sns_topic")
	f.environment_of(t) != "development"
	topic_has_key(t)
	not topic_has_cmk(t)
	not f.exempt("iacp.encryption.sns_kms", t.address)
	m := f.msg(
		"iacp.encryption.sns_kms", t.address,
		sprintf("topic in environment %q uses the AWS-managed aws/sns key", [f.environment_of(t)]),
		"set kms_master_key_id to a customer-managed key (CloudWatch alarms cannot publish to alias/aws/sns topics)",
	)
}

# --- DynamoDB ----------------------------------------------------------------

# A table without an enabled server_side_encryption block uses the AWS owned
# key; enabled without kms_key_arn uses the AWS-managed aws/dynamodb key. The
# provider computes kms_key_arn in both cases, so an unknown planned key only
# counts as a customer-managed key created in the same plan when the
# configuration sets kms_key_arn.
table_has_cmk(t) if {
	some sse in object.get(f.after(t), "server_side_encryption", [])
	sse.enabled == true
	customer_managed_key(object.get(sse, "kms_key_arn", null))
}

table_has_cmk(t) if {
	some i, sse in object.get(f.after(t), "server_side_encryption", [])
	sse.enabled == true
	unknown_at(t, ["server_side_encryption", i, "kms_key_arn"])
	config_sets_table_key(t)
}

config_sets_table_key(t) if {
	some block in f.config_expr(t, "server_side_encryption")
	key := object.get(block, "kms_key_arn", {})
	count(object.get(key, "references", [])) > 0
}

deny contains m if {
	some t in f.changed_of_type("aws_dynamodb_table")
	f.environment_of(t) != "development"
	not table_has_cmk(t)
	not f.exempt("iacp.encryption.dynamodb_cmk", t.address)
	m := f.msg(
		"iacp.encryption.dynamodb_cmk", t.address,
		sprintf("table in environment %q is not encrypted with a customer-managed KMS key", [f.environment_of(t)]),
		"add server_side_encryption { enabled = true, kms_key_arn = <customer-managed key> }",
	)
}

# --- KMS ---------------------------------------------------------------------

deny contains m if {
	some k in f.changed_of_type("aws_kms_key")
	f.after(k).enable_key_rotation != true
	not f.exempt("iacp.encryption.kms_rotation", k.address)
	m := f.msg(
		"iacp.encryption.kms_rotation", k.address,
		"KMS key rotation is disabled",
		"set enable_key_rotation = true",
	)
}
