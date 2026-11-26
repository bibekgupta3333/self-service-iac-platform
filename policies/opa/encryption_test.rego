package iacp.encryption_test

import data.iacp.encryption as enc
import data.lib.testing as t

bucket := t.rc_in("data", "aws_s3_bucket", "this", {"bucket": "orders-data"})

cmk := "arn:aws:kms:us-east-1:111111111111:key/1234"

sse(algorithm) := sse_with_key(algorithm, cmk)

sse_with_key(algorithm, key) := t.rc_in("data", "aws_s3_bucket_server_side_encryption_configuration", "this", {
	"bucket": "orders-data",
	"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": algorithm, "kms_master_key_id": key}]}],
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

test_s3_unchanged_bucket_with_sse_deleted_denied if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	count(enc.deny) == 1 with input as t.plan([noop_bucket, t.with_actions(sse("aws:kms"), ["delete"])])
}

test_s3_unchanged_bucket_with_sse_downgraded_denied if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	count(enc.deny) == 1 with input as t.plan([noop_bucket, t.with_actions(sse("AES256"), ["update"])])
}

test_s3_unchanged_bucket_and_sse_allowed if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	count(enc.deny) == 0 with input as t.plan([noop_bucket, t.with_actions(sse("aws:kms"), ["no-op"])])
}

test_s3_deleted_bucket_ignored if {
	count(enc.deny) == 0 with input as t.plan([t.with_actions(bucket, ["delete"])])
}

test_s3_inline_kms_allowed if {
	b := t.rc("aws_s3_bucket", "legacy", {"server_side_encryption_configuration": [{"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": "aws:kms", "kms_master_key_id": cmk}]}]}]})
	count(enc.deny) == 0 with input as t.plan([b])
}

test_s3_aws_managed_key_denied_outside_development if {
	inp := t.plan([bucket, sse_with_key("aws:kms", null)])
	some m in enc.deny with input as inp with data.iacp.context as t.prod_ctx
	m == concat("", [
		`[iacp.encryption.s3_cmk] module.data.aws_s3_bucket.this: bucket in environment "production" uses the AWS-managed aws/s3 key. `,
		"Fix: set kms_master_key_id to a customer-managed KMS key",
	])
}

test_s3_aws_managed_alias_denied_outside_development if {
	inp := t.plan([bucket, sse_with_key("aws:kms", "alias/aws/s3")])
	rule_ids(enc.deny) == {"[iacp.encryption.s3_cmk]"} with input as inp with data.iacp.context as t.prod_ctx
}

test_s3_aws_managed_key_allowed_in_development if {
	count(enc.deny) == 0 with input as t.plan([bucket, sse_with_key("aws:kms", null)]) with data.iacp.context as t.dev_ctx
}

test_s3_key_created_in_same_plan_allowed if {
	unknown := {"rule": [{"apply_server_side_encryption_by_default": [{"kms_master_key_id": true}]}]}
	s := t.with_unknown(sse_with_key("aws:kms", null), unknown)
	count(enc.deny) == 0 with input as t.plan([bucket, s]) with data.iacp.context as t.prod_ctx
}

test_s3_inline_aws_managed_key_denied_outside_development if {
	b := t.rc("aws_s3_bucket", "legacy", {"server_side_encryption_configuration": [{"rule": [{"apply_server_side_encryption_by_default": [{"sse_algorithm": "aws:kms"}]}]}]})
	rule_ids(enc.deny) == {"[iacp.encryption.s3_cmk]"} with input as t.plan([b]) with data.iacp.context as t.prod_ctx
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

test_sqs_aws_managed_alias_denied_in_production if {
	q := queue({"kms_master_key_id": "alias/aws/sqs"})
	rule_ids(enc.deny) == {"[iacp.encryption.sqs_kms]"} with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_aws_managed_alias_arn_denied_in_production if {
	q := queue({"kms_master_key_id": "arn:aws:kms:us-east-1:111111111111:alias/aws/sqs"})
	count(enc.deny) == 1 with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_aws_managed_alias_allowed_in_development if {
	q := queue({"kms_master_key_id": "alias/aws/sqs"})
	count(enc.deny) == 0 with input as t.plan([q]) with data.iacp.context as t.dev_ctx
}

test_sqs_customer_alias_allowed_in_production if {
	q := queue({"kms_master_key_id": "alias/iacp-orders"})
	count(enc.deny) == 0 with input as t.plan([q]) with data.iacp.context as t.prod_ctx
}

test_sqs_unencrypted_denied if {
	q := queue({"sqs_managed_sse_enabled": false})
	rule_ids(enc.deny) == {"[iacp.encryption.sqs_sse]"} with input as t.plan([q]) with data.iacp.context as t.dev_ctx
}

# --- SNS -----------------------------------------------------------------------

topic(after) := t.rc("aws_sns_topic", "alarms", after)

test_sns_with_cmk_allowed_in_production if {
	tp := topic({"kms_master_key_id": cmk})
	count(enc.deny) == 0 with input as t.plan([tp]) with data.iacp.context as t.prod_ctx
}

test_sns_with_key_created_in_same_plan_allowed if {
	tp := t.with_unknown(topic({}), {"kms_master_key_id": true})
	count(enc.deny) == 0 with input as t.plan([tp]) with data.iacp.context as t.prod_ctx
}

test_sns_unencrypted_denied_in_development if {
	some m in enc.deny with input as t.plan([topic({"kms_master_key_id": null})]) with data.iacp.context as t.dev_ctx
	m == concat("", [
		"[iacp.encryption.sns_sse] aws_sns_topic.alarms: topic has no server-side encryption. ",
		"Fix: set kms_master_key_id to a customer-managed key (alias/aws/sns is only allowed in development)",
	])
}

test_sns_empty_key_denied if {
	rule_ids(enc.deny) == {"[iacp.encryption.sns_sse]"} with input as t.plan([topic({"kms_master_key_id": ""})]) with data.iacp.context as t.prod_ctx
}

test_sns_aws_managed_alias_denied_in_production if {
	tp := topic({"kms_master_key_id": "alias/aws/sns"})
	some m in enc.deny with input as t.plan([tp]) with data.iacp.context as t.prod_ctx
	startswith(m, `[iacp.encryption.sns_kms] aws_sns_topic.alarms: topic in environment "production" uses the AWS-managed aws/sns key.`)
}

test_sns_aws_managed_alias_arn_denied_without_environment if {
	tp := topic({"kms_master_key_id": "arn:aws:kms:us-east-1:111111111111:alias/aws/sns"})
	rule_ids(enc.deny) == {"[iacp.encryption.sns_kms]"} with input as t.plan([tp])
}

test_sns_aws_managed_alias_allowed_in_development if {
	count(enc.deny) == 0 with input as t.plan([topic({"kms_master_key_id": "alias/aws/sns"})]) with data.iacp.context as t.dev_ctx
}

test_sns_unchanged_topic_ignored if {
	tp := t.with_actions(topic({}), ["no-op"])
	count(enc.deny) == 0 with input as t.plan([tp]) with data.iacp.context as t.prod_ctx
}

test_sns_exception_waives_rule if {
	exc := {"rule_id": "iacp.encryption.sns_kms", "resource_address": "aws_sns_topic.alarms", "expires_at": "2200-01-01T00:00:00Z"}
	ctx := object.union(t.prod_ctx, {"exceptions": [exc]})
	count(enc.deny) == 0 with input as t.plan([topic({"kms_master_key_id": "alias/aws/sns"})]) with data.iacp.context as ctx
}

# --- DynamoDB ------------------------------------------------------------------

table(sse) := t.rc_in("orders", "aws_dynamodb_table", "this", {"name": "orders", "server_side_encryption": sse})

key_ref := {"server_side_encryption": [{"enabled": {"constant_value": true}, "kms_key_arn": {"references": ["local.kms_key_arn"]}}]}

table_config(expressions) := {"configuration": {"root_module": {"module_calls": {"orders": {"module": {"resources": [t.cfg("aws_dynamodb_table", "this", expressions)]}}}}}}

with_config(rcs, expressions) := object.union(t.plan(rcs), table_config(expressions))

test_dynamodb_with_cmk_allowed_in_production if {
	tb := table([{"enabled": true, "kms_key_arn": cmk}])
	count(enc.deny) == 0 with input as t.plan([tb]) with data.iacp.context as t.prod_ctx
}

test_dynamodb_with_configured_key_created_in_same_plan_allowed if {
	tb := t.with_unknown(table([{"enabled": true}]), {"server_side_encryption": [{"kms_key_arn": true}]})
	count(enc.deny) == 0 with input as with_config([tb], key_ref) with data.iacp.context as t.prod_ctx
}

test_dynamodb_unknown_key_without_configured_key_denied if {
	# enabled = true without kms_key_arn: the provider computes the aws/dynamodb key.
	tb := t.with_unknown(table([{"enabled": true}]), {"server_side_encryption": [{"kms_key_arn": true}]})
	no_key := {"server_side_encryption": [{"enabled": {"constant_value": true}}]}
	rule_ids(enc.deny) == {"[iacp.encryption.dynamodb_cmk]"} with input as with_config([tb], no_key) with data.iacp.context as t.prod_ctx
}

test_dynamodb_without_sse_block_denied_in_staging if {
	tb := table([])
	some m in enc.deny with input as t.plan([tb]) with data.iacp.context as object.union(t.prod_ctx, {"environment": "staging"})
	m == concat("", [
		`[iacp.encryption.dynamodb_cmk] module.orders.aws_dynamodb_table.this: table in environment "staging" is not encrypted with a customer-managed KMS key. `,
		"Fix: add server_side_encryption { enabled = true, kms_key_arn = <customer-managed key> }",
	])
}

test_dynamodb_sse_disabled_denied_without_environment if {
	tb := table([{"enabled": false, "kms_key_arn": ""}])
	rule_ids(enc.deny) == {"[iacp.encryption.dynamodb_cmk]"} with input as t.plan([tb])
}

test_dynamodb_aws_managed_key_denied_in_production if {
	tb := table([{"enabled": true, "kms_key_arn": "arn:aws:kms:us-east-1:111111111111:alias/aws/dynamodb"}])
	rule_ids(enc.deny) == {"[iacp.encryption.dynamodb_cmk]"} with input as t.plan([tb]) with data.iacp.context as t.prod_ctx
}

test_dynamodb_aws_owned_key_allowed_in_development if {
	count(enc.deny) == 0 with input as t.plan([table([])]) with data.iacp.context as t.dev_ctx
}

test_dynamodb_unchanged_table_ignored if {
	tb := t.with_actions(table([]), ["no-op"])
	count(enc.deny) == 0 with input as t.plan([tb]) with data.iacp.context as t.prod_ctx
}

# --- KMS -----------------------------------------------------------------------

test_kms_rotation_enabled_allowed if {
	count(enc.deny) == 0 with input as t.plan([t.rc("aws_kms_key", "k", {"enable_key_rotation": true})])
}

test_kms_rotation_disabled_denied if {
	some m in enc.deny with input as t.plan([t.rc("aws_kms_key", "k", {"enable_key_rotation": false})])
	startswith(m, "[iacp.encryption.kms_rotation] aws_kms_key.k")
}

# --- RDS -----------------------------------------------------------------------

db(after) := t.rc("aws_db_instance", "db", after)

kms_ref := {"kms_key_id": {"references": ["local.kms_key_arn"]}}

test_rds_encrypted_with_cmk_allowed_in_production if {
	inp := t.plan([db({"storage_encrypted": true, "kms_key_id": cmk})])
	count(enc.deny) == 0 with input as inp with data.iacp.context as t.prod_ctx
}

test_rds_key_created_in_same_plan_allowed_in_production if {
	d := t.with_unknown(db({"storage_encrypted": true}), {"kms_key_id": true})
	inp := t.plan_with_config([d], [t.cfg("aws_db_instance", "db", kms_ref)])
	count(enc.deny) == 0 with input as inp with data.iacp.context as t.prod_ctx
}

test_rds_unencrypted_denied if {
	some m in enc.deny with input as t.plan([db({"storage_encrypted": false})]) with data.iacp.context as t.dev_ctx
	m == concat("", [
		"[iacp.encryption.rds_storage] aws_db_instance.db: database storage is not encrypted. ",
		"Fix: set storage_encrypted = true with kms_key_id set to a customer-managed key (the postgres module does this)",
	])
}

test_rds_cluster_without_storage_encrypted_denied if {
	c := t.rc("aws_rds_cluster", "c", {})
	t.rule_ids(enc.deny) == {"[iacp.encryption.rds_storage]"} with input as t.plan([c]) with data.iacp.context as t.dev_ctx
}

# kms_key_id is computed when omitted (AWS picks aws/rds); without a configured
# expression that must not count as a customer-managed key.
test_rds_default_key_denied_in_production if {
	d := t.with_unknown(db({"storage_encrypted": true}), {"kms_key_id": true})
	t.rule_ids(enc.deny) == {"[iacp.encryption.rds_cmk]"} with input as t.plan([d]) with data.iacp.context as t.prod_ctx
}

test_rds_aws_managed_alias_denied_in_staging if {
	inp := t.plan([db({"storage_encrypted": true, "kms_key_id": "arn:aws:kms:us-east-1:111111111111:alias/aws/rds"})])
	some m in enc.deny with input as inp with data.iacp.context as object.union(t.prod_ctx, {"environment": "staging"})
	contains(m, `[iacp.encryption.rds_cmk] aws_db_instance.db: database in environment "staging"`)
}

test_rds_default_key_allowed_in_development if {
	d := t.with_unknown(db({"storage_encrypted": true}), {"kms_key_id": true})
	count(enc.deny) == 0 with input as t.plan([d]) with data.iacp.context as t.dev_ctx
}

test_rds_unchanged_instance_ignored if {
	d := t.with_actions(db({"storage_encrypted": false}), ["no-op"])
	count(enc.deny) == 0 with input as t.plan([d]) with data.iacp.context as t.prod_ctx
}

# --- ElastiCache ---------------------------------------------------------------

cache(after) := t.rc("aws_elasticache_replication_group", "cache", object.union(
	{"at_rest_encryption_enabled": "true", "transit_encryption_enabled": true, "kms_key_id": cmk},
	after,
))

test_elasticache_encrypted_allowed_in_production if {
	count(enc.deny) == 0 with input as t.plan([cache({})]) with data.iacp.context as t.prod_ctx
}

test_elasticache_boolean_at_rest_flag_allowed if {
	count(enc.deny) == 0 with input as t.plan([cache({"at_rest_encryption_enabled": true})]) with data.iacp.context as t.prod_ctx
}

test_elasticache_key_created_in_same_plan_allowed if {
	c := t.with_unknown(cache({"kms_key_id": null}), {"kms_key_id": true})
	inp := t.plan_with_config([c], [t.cfg("aws_elasticache_replication_group", "cache", kms_ref)])
	count(enc.deny) == 0 with input as inp with data.iacp.context as t.prod_ctx
}

test_elasticache_at_rest_disabled_denied if {
	some m in enc.deny with input as t.plan([cache({"at_rest_encryption_enabled": "false"})]) with data.iacp.context as t.dev_ctx
	startswith(m, "[iacp.encryption.elasticache_at_rest] aws_elasticache_replication_group.cache: cache data is not encrypted at rest. Fix:")
}

test_elasticache_transit_disabled_denied if {
	ids := t.rule_ids(enc.deny) with input as t.plan([cache({"transit_encryption_enabled": false})]) with data.iacp.context as t.dev_ctx
	ids == {"[iacp.encryption.elasticache_transit]"}
}

test_elasticache_transit_missing_denied if {
	c := t.rc("aws_elasticache_replication_group", "cache", {"at_rest_encryption_enabled": "true", "kms_key_id": cmk})
	t.rule_ids(enc.deny) == {"[iacp.encryption.elasticache_transit]"} with input as t.plan([c]) with data.iacp.context as t.dev_ctx
}

test_elasticache_aws_managed_key_denied_in_production if {
	ids := t.rule_ids(enc.deny) with input as t.plan([cache({"kms_key_id": null})]) with data.iacp.context as t.prod_ctx
	ids == {"[iacp.encryption.elasticache_cmk]"}
}

test_elasticache_aws_managed_key_allowed_in_development if {
	count(enc.deny) == 0 with input as t.plan([cache({"kms_key_id": null})]) with data.iacp.context as t.dev_ctx
}

# --- ECR -----------------------------------------------------------------------

repo(encryption) := t.rc("aws_ecr_repository", "repo", {
	"encryption_configuration": encryption,
	"image_scanning_configuration": [{"scan_on_push": true}],
	"image_tag_mutability": "IMMUTABLE",
})

test_ecr_kms_allowed_in_production if {
	r := repo([{"encryption_type": "KMS", "kms_key": cmk}])
	count(enc.deny) == 0 with input as t.plan([r]) with data.iacp.context as t.prod_ctx
}

test_ecr_aes256_denied_in_production if {
	r := repo([{"encryption_type": "AES256", "kms_key": ""}])
	some m in enc.deny with input as t.plan([r]) with data.iacp.context as t.prod_ctx
	startswith(m, `[iacp.encryption.ecr_kms] aws_ecr_repository.repo: repository in environment "production" is not encrypted with a customer-managed KMS key. Fix:`)
}

test_ecr_without_encryption_block_denied_in_production if {
	t.rule_ids(enc.deny) == {"[iacp.encryption.ecr_kms]"} with input as t.plan([repo([])]) with data.iacp.context as t.prod_ctx
}

# KMS without kms_key is the AWS-managed aws/ecr key.
test_ecr_kms_with_empty_key_denied_in_production if {
	r := repo([{"encryption_type": "KMS", "kms_key": ""}])
	t.rule_ids(enc.deny) == {"[iacp.encryption.ecr_kms]"} with input as t.plan([r]) with data.iacp.context as t.prod_ctx
}

test_ecr_kms_with_aws_managed_alias_denied_in_staging if {
	r := repo([{"encryption_type": "KMS", "kms_key": "arn:aws:kms:us-east-1:111111111111:alias/aws/ecr"}])
	ctx := object.union(t.prod_ctx, {"environment": "staging"})
	t.rule_ids(enc.deny) == {"[iacp.encryption.ecr_kms]"} with input as t.plan([r]) with data.iacp.context as ctx
}

# The provider reports an omitted kms_key as computed; without a configured
# expression that is the AWS-managed key.
test_ecr_kms_with_unknown_unconfigured_key_denied_in_production if {
	r := t.with_unknown(repo([{"encryption_type": "KMS"}]), {"encryption_configuration": [{"kms_key": true}]})
	t.rule_ids(enc.deny) == {"[iacp.encryption.ecr_kms]"} with input as t.plan([r]) with data.iacp.context as t.prod_ctx
}

test_ecr_kms_key_created_in_same_plan_allowed_in_production if {
	r := t.with_unknown(repo([{"encryption_type": "KMS"}]), {"encryption_configuration": [{"kms_key": true}]})
	config := t.cfg("aws_ecr_repository", "repo", {"encryption_configuration": [{
		"encryption_type": {"constant_value": "KMS"},
		"kms_key": {"references": ["local.kms_key_arn", "local"]},
	}]})
	count(enc.deny) == 0 with input as t.plan_with_config([r], [config]) with data.iacp.context as t.prod_ctx
}

test_ecr_kms_with_empty_key_allowed_in_development if {
	r := repo([{"encryption_type": "KMS", "kms_key": ""}])
	count(enc.deny) == 0 with input as t.plan([r]) with data.iacp.context as t.dev_ctx
}

test_ecr_aes256_allowed_in_development if {
	r := repo([{"encryption_type": "AES256"}])
	count(enc.deny) == 0 with input as t.plan([r]) with data.iacp.context as t.dev_ctx
}
