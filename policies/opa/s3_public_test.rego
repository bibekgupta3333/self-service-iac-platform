package iacp.s3_test

import data.iacp.s3
import data.lib.testing as t

bucket := t.rc_in("data", "aws_s3_bucket", "this", {"bucket": "orders-data"})

pab(overrides) := t.rc_in("data", "aws_s3_bucket_public_access_block", "this", object.union(
	{
		"bucket": "orders-data",
		"block_public_acls": true,
		"block_public_policy": true,
		"ignore_public_acls": true,
		"restrict_public_buckets": true,
	},
	overrides,
))

bucket_policy(doc) := t.rc_in("data", "aws_s3_bucket_policy", "this", {"policy": json.marshal(doc)})

allow_all(condition) := {"Version": "2012-10-17", "Statement": [object.union(
	{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::orders-data/*"},
	condition,
)]}

# --- Public access block ---------------------------------------------------------

test_bucket_with_full_pab_allowed if {
	count(s3.deny) == 0 with input as t.plan([bucket, pab({})])
}

test_bucket_without_pab_denied if {
	some m in s3.deny with input as t.plan([bucket])
	startswith(m, "[iacp.s3.no_public] module.data.aws_s3_bucket.this: ")
}

test_bucket_with_partial_pab_denied if {
	count(s3.deny) == 1 with input as t.plan([bucket, pab({"restrict_public_buckets": false})])
}

test_pab_exception_honoured if {
	ctx := {"now": "2026-01-01T00:00:00Z", "exceptions": [{
		"rule_id": "iacp.s3.no_public",
		"resource_address": "module.data.aws_s3_bucket.this",
		"expires_at": "2026-06-01T00:00:00Z",
	}]}
	count(s3.deny) == 0 with input as t.plan([bucket]) with data.iacp.context as ctx
}

# --- ACLs ------------------------------------------------------------------------

test_private_acl_allowed if {
	acl := t.rc_in("data", "aws_s3_bucket_acl", "this", {"acl": "private"})
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), acl])
}

test_public_acl_denied if {
	acl := t.rc_in("data", "aws_s3_bucket_acl", "this", {"acl": "public-read"})
	some m in s3.deny with input as t.plan([bucket, pab({}), acl])
	contains(m, `[iacp.s3.no_public_acl] module.data.aws_s3_bucket_acl.this: canned ACL "public-read"`)
}

test_public_inline_bucket_acl_denied if {
	b := t.rc_in("data", "aws_s3_bucket", "this", {"bucket": "orders-data", "acl": "public-read-write"})
	count(s3.deny) == 1 with input as t.plan([b, pab({})])
}

# --- Bucket policies -------------------------------------------------------------

test_policy_scoped_principal_allowed if {
	doc := {"Statement": [{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::111111111111:root"}, "Action": "s3:GetObject"}]}
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_wildcard_with_org_condition_allowed if {
	doc := allow_all({"Condition": {"StringEquals": {"aws:PrincipalOrgID": "o-abc123"}}})
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_wildcard_deny_statement_allowed if {
	doc := {"Statement": {"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Condition": {"Bool": {"aws:SecureTransport": "false"}}}}
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_wildcard_principal_denied if {
	some m in s3.deny with input as t.plan([bucket, pab({}), bucket_policy(allow_all({}))])
	startswith(m, "[iacp.s3.no_public_policy] module.data.aws_s3_bucket_policy.this: ")
}

test_policy_wildcard_aws_principal_list_denied if {
	doc := {"Statement": [{"Effect": "Allow", "Principal": {"AWS": ["*"]}, "Action": "s3:GetObject"}]}
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_irrelevant_condition_denied if {
	doc := allow_all({"Condition": {"Bool": {"aws:SecureTransport": "true"}}})
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}
