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

test_s3_two_bucket_probe_only_unlinked_bucket_denied if {
	a := t.rc("aws_s3_bucket", "a", {"bucket": "probe-a"})
	b := t.rc("aws_s3_bucket", "b", {"bucket": "probe-b"})
	pab_a := t.rc("aws_s3_bucket_public_access_block", "a", object.remove(pab({}).change.after, ["bucket"]))
	inp := t.plan_with_config([a, b, pab_a], [t.cfg(pab_a.type, "a", t.bucket_ref("a"))])
	s3.deny == {concat("", [
		"[iacp.s3.no_public] aws_s3_bucket.b: bucket has no public access block with all four flags enabled. ",
		`Fix: add aws_s3_bucket_public_access_block with ["block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"] all set to true`,
	])} with input as inp
}

test_unchanged_bucket_with_pab_weakened_denied if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	weakened := t.with_actions(pab({"block_public_policy": false}), ["update"])
	some m in s3.deny with input as t.plan([noop_bucket, weakened])
	startswith(m, "[iacp.s3.no_public] module.data.aws_s3_bucket.this:")
}

test_unchanged_bucket_with_pab_deleted_denied if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	count(s3.deny) == 1 with input as t.plan([noop_bucket, t.with_actions(pab({}), ["delete"])])
}

test_unchanged_bucket_and_pab_allowed if {
	noop_bucket := t.with_actions(bucket, ["no-op"])
	count(s3.deny) == 0 with input as t.plan([noop_bucket, t.with_actions(pab({}), ["no-op"])])
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

test_policy_not_principal_denied if {
	doc := {"Statement": [{"Effect": "Allow", "NotPrincipal": {"AWS": "arn:aws:iam::111111111111:root"}, "Action": "s3:GetObject"}]}
	some m in s3.deny with input as t.plan([bucket, pab({}), bucket_policy(doc)])
	contains(m, `policy allows public access (Principal "*" or NotPrincipal)`)
}

test_policy_wildcard_condition_value_denied if {
	doc := allow_all({"Condition": {"StringLike": {"aws:SourceArn": "*"}}})
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_negated_condition_denied if {
	doc := allow_all({"Condition": {"StringNotEquals": {"aws:SourceAccount": "111111111111"}}})
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_if_exists_condition_denied if {
	doc := allow_all({"Condition": {"StringEqualsIfExists": {"aws:PrincipalOrgID": "o-abc123"}}})
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

test_policy_source_account_condition_allowed if {
	doc := allow_all({"Condition": {"StringEquals": {"aws:SourceAccount": ["111111111111"]}}})
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}

acl_grant(uri) := t.rc_in("data", "aws_s3_bucket_acl", "this", {"access_control_policy": [{"grant": [{
	"permission": "READ",
	"grantee": [{"type": "Group", "uri": uri}],
}]}]})

test_public_acl_grant_denied if {
	some m in s3.deny with input as t.plan([bucket, pab({}), acl_grant("http://acs.amazonaws.com/groups/global/AllUsers")])
	m == concat("", [
		"[iacp.s3.no_public_acl] module.data.aws_s3_bucket_acl.this: ACL grants READ to http://acs.amazonaws.com/groups/global/AllUsers. ",
		"Fix: remove the public grant (use bucket owner enforced object ownership)",
	])
}

test_authenticated_users_grant_denied if {
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), acl_grant("http://acs.amazonaws.com/groups/global/AuthenticatedUsers")])
}

test_log_delivery_grant_allowed if {
	count(s3.deny) == 0 with input as t.plan([bucket, pab({}), acl_grant("http://acs.amazonaws.com/groups/s3/LogDelivery")])
}

test_computed_bucket_policy_warns if {
	p := t.computed_policy("aws_s3_bucket_policy", "p")
	count(s3.deny) == 0 with input as t.plan([p])
	t.rule_ids(s3.warn) == {"[iacp.s3.policy_unknown]"} with input as t.plan([p])
}

# Principal "*" with Resource = "${aws_s3_bucket.a.arn}/*" via a policy document.
test_policy_document_wildcard_principal_denied if {
	p := t.computed_policy("aws_s3_bucket_policy", "p")
	d := t.policy_doc_data("d", [{
		"effect": "Allow", "actions": ["s3:GetObject"], "resources": [null],
		"principals": [{"type": "*", "identifiers": ["*"]}], "condition": [],
	}])
	inp := t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
	t.rule_ids(s3.deny) == {"[iacp.s3.no_public_policy]"} with input as inp
	count(s3.warn) == 0 with input as inp
}

test_policy_document_aws_wildcard_principal_denied if {
	p := t.computed_policy("aws_s3_bucket_policy", "p")
	d := t.policy_doc_data("d", [{"actions": ["s3:GetObject"], "principals": [{"type": "AWS", "identifiers": ["*"]}]}])
	count(s3.deny) == 1 with input as t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
}

test_policy_document_org_condition_allowed if {
	p := t.computed_policy("aws_s3_bucket_policy", "p")
	d := t.policy_doc_data("d", [{
		"actions": ["s3:GetObject"], "principals": [{"type": "*", "identifiers": ["*"]}],
		"condition": [{"test": "StringEquals", "variable": "aws:PrincipalOrgID", "values": ["o-abc123"]}],
	}])
	count(s3.deny) == 0 with input as t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
}

test_policy_document_account_principal_allowed if {
	p := t.computed_policy("aws_s3_bucket_policy", "p")
	d := t.policy_doc_data("d", [{"actions": ["s3:GetObject"], "principals": [{"type": "AWS", "identifiers": ["arn:aws:iam::111111111111:root"]}]}])
	count(s3.deny) == 0 with input as t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
}

test_policy_irrelevant_condition_denied if {
	doc := allow_all({"Condition": {"Bool": {"aws:SecureTransport": "true"}}})
	count(s3.deny) == 1 with input as t.plan([bucket, pab({}), bucket_policy(doc)])
}
