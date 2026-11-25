# METADATA
# title: Encryption at rest
# description: |
#   S3 buckets must use SSE-KMS, SQS queues must be encrypted (customer-managed
#   KMS outside development) and KMS keys must rotate.
package iacp.encryption

import data.lib.iacp as f

kms_algorithms := {"aws:kms", "aws:kms:dsse"}

# --- S3 ----------------------------------------------------------------------

# uses_kms(rules) is true when any SSE rule defaults to a KMS algorithm.
uses_kms(rules) if {
	some rule in rules
	some d in rule.apply_server_side_encryption_by_default
	d.sse_algorithm in kms_algorithms
}

# linked(child, bucket): child config is in the bucket's module and, when both
# names are known at plan time, targets the same bucket.
linked(child, bucket) if {
	f.same_module(child, bucket)
	not bucket_mismatch(child, bucket)
}

bucket_mismatch(child, bucket) if f.after(child).bucket != f.after(bucket).bucket

bucket_has_kms(b) if {
	some sse in f.present_of_type("aws_s3_bucket_server_side_encryption_configuration")
	linked(sse, b)
	uses_kms(f.after(sse).rule)
}

# Legacy inline configuration on aws_s3_bucket.
bucket_has_kms(b) if {
	some cfg in f.after(b).server_side_encryption_configuration
	uses_kms(cfg.rule)
}

deny contains m if {
	some b in f.changed_of_type("aws_s3_bucket")
	not bucket_has_kms(b)
	not f.exempt("iacp.encryption.s3_kms", b.address)
	m := f.msg(
		"iacp.encryption.s3_kms", b.address,
		"bucket is not encrypted with SSE-KMS",
		`add aws_s3_bucket_server_side_encryption_configuration with sse_algorithm = "aws:kms" and a customer-managed key`,
	)
}

# --- SQS ---------------------------------------------------------------------

has_kms_key(q) if {
	key := f.after(q).kms_master_key_id
	is_string(key)
	key != ""
}

has_kms_key(q) if f.after_unknown(q, "kms_master_key_id")

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
	not has_kms_key(q)
	not f.exempt("iacp.encryption.sqs_kms", q.address)
	m := f.msg(
		"iacp.encryption.sqs_kms", q.address,
		sprintf("queue in environment %q is not encrypted with a customer-managed KMS key", [f.environment_of(q)]),
		"set kms_master_key_id; SQS-managed SSE is only allowed in development",
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
