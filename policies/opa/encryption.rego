# METADATA
# title: Encryption at rest
# description: |
#   S3 buckets must use SSE-KMS and SQS queues must be encrypted; outside
#   development both need a customer-managed key (not alias/aws/*). RDS
#   storage and ElastiCache data must be encrypted at rest (with a
#   customer-managed key outside development), ElastiCache must require TLS
#   in transit, and ECR repositories outside development must use KMS. KMS
#   keys must rotate.
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

# --- RDS ---------------------------------------------------------------------

rds_types := {"aws_db_instance", "aws_rds_cluster"}

rds_storage contains db if {
	some db in f.changed
	db.type in rds_types
}

# configured_cmk(rc): rc's kms_key_id is a customer-managed key, or is computed
# at apply time from a configured expression (a key created in the same plan).
# RDS also reports kms_key_id as computed when it is omitted (AWS then picks
# aws/rds), so an unknown value only counts when the configuration sets it.
configured_cmk(rc) if customer_managed_key(object.get(f.after(rc), "kms_key_id", null))

configured_cmk(rc) if {
	unknown_at(rc, ["kms_key_id"])
	f.config_expr(rc, "kms_key_id") != {}
}

deny contains m if {
	some db in rds_storage
	not f.flag(db, "storage_encrypted")
	not f.exempt("iacp.encryption.rds_storage", db.address)
	m := f.msg(
		"iacp.encryption.rds_storage", db.address,
		"database storage is not encrypted",
		"set storage_encrypted = true with kms_key_id set to a customer-managed key (the postgres module does this)",
	)
}

deny contains m if {
	some db in rds_storage
	f.environment_of(db) != "development"
	f.flag(db, "storage_encrypted")
	not configured_cmk(db)
	not f.exempt("iacp.encryption.rds_cmk", db.address)
	m := f.msg(
		"iacp.encryption.rds_cmk", db.address,
		sprintf("database in environment %q is encrypted with the AWS-managed aws/rds key", [f.environment_of(db)]),
		"set kms_key_id to a customer-managed KMS key",
	)
}

# --- ElastiCache ---------------------------------------------------------------

deny contains m if {
	some rg in f.changed_of_type("aws_elasticache_replication_group")
	not f.flag(rg, "at_rest_encryption_enabled")
	not f.exempt("iacp.encryption.elasticache_at_rest", rg.address)
	m := f.msg(
		"iacp.encryption.elasticache_at_rest", rg.address,
		"cache data is not encrypted at rest",
		"set at_rest_encryption_enabled = true with kms_key_id set to a customer-managed key (the redis module does this)",
	)
}

deny contains m if {
	some rg in f.changed_of_type("aws_elasticache_replication_group")
	f.environment_of(rg) != "development"
	f.flag(rg, "at_rest_encryption_enabled")
	not configured_cmk(rg)
	not f.exempt("iacp.encryption.elasticache_cmk", rg.address)
	m := f.msg(
		"iacp.encryption.elasticache_cmk", rg.address,
		sprintf("cache in environment %q is encrypted with the AWS-managed key", [f.environment_of(rg)]),
		"set kms_key_id to a customer-managed KMS key",
	)
}

deny contains m if {
	some rg in f.changed_of_type("aws_elasticache_replication_group")
	not f.flag(rg, "transit_encryption_enabled")
	not f.exempt("iacp.encryption.elasticache_transit", rg.address)
	m := f.msg(
		"iacp.encryption.elasticache_transit", rg.address,
		"cache accepts connections without TLS",
		`set transit_encryption_enabled = true (and transit_encryption_mode = "required")`,
	)
}

# --- ECR -----------------------------------------------------------------------

ecr_kms(repo) if {
	some c in f.after(repo).encryption_configuration
	c.encryption_type == "KMS"
}

deny contains m if {
	some repo in f.changed_of_type("aws_ecr_repository")
	f.environment_of(repo) != "development"
	not ecr_kms(repo)
	not f.exempt("iacp.encryption.ecr_kms", repo.address)
	m := f.msg(
		"iacp.encryption.ecr_kms", repo.address,
		sprintf("repository in environment %q is not encrypted with KMS", [f.environment_of(repo)]),
		`add encryption_configuration { encryption_type = "KMS", kms_key = <customer-managed key> } (the ecs-service module does this)`,
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
