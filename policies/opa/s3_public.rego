# METADATA
# title: No public S3
# description: |
#   Every bucket needs a full public access block, no public ACLs and no
#   bucket policy that allows Principal "*" without a restricting condition.
package iacp.s3

import data.lib.iacp as f

pab_flags := ["block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"]

public_acls := {"public-read", "public-read-write", "authenticated-read"}

full_pab(p) if every flag in pab_flags { f.after(p)[flag] == true }

has_full_pab(b) if {
	some p in f.present_of_type("aws_s3_bucket_public_access_block")
	f.linked_to_bucket(p, b)
	full_pab(p)
}

deny contains m if {
	# Every bucket that will exist, so disabling a control on an unchanged bucket is caught.
	some b in f.present_of_type("aws_s3_bucket")
	not has_full_pab(b)
	not f.exempt("iacp.s3.no_public", b.address)
	m := f.msg(
		"iacp.s3.no_public", b.address,
		"bucket has no public access block with all four flags enabled",
		sprintf("add aws_s3_bucket_public_access_block with %v all set to true", [pab_flags]),
	)
}

# --- ACLs ----------------------------------------------------------------------

deny contains m if {
	some rc in f.changed
	rc.type in {"aws_s3_bucket_acl", "aws_s3_bucket"}
	acl := f.after(rc).acl
	acl in public_acls
	not f.exempt("iacp.s3.no_public_acl", rc.address)
	m := f.msg(
		"iacp.s3.no_public_acl", rc.address,
		sprintf("canned ACL %q makes the bucket public", [acl]),
		`remove the ACL (use bucket owner enforced object ownership) or use "private"`,
	)
}

# --- Bucket policies -------------------------------------------------------------

restricting_condition_keys := {"aws:principalorgid", "aws:sourcearn", "aws:sourceaccount", "aws:sourcevpce", "aws:sourcevpc"}

wildcard_principal("*")

wildcard_principal(p) if "*" in f.as_array(p.AWS)

restricted(statement) if {
	some _, conditions in statement.Condition
	some key, _ in conditions
	lower(key) in restricting_condition_keys
}

public_statements(p) := [s |
	some doc in f.policy_documents(p, "policy")
	some s in f.as_array(doc.Statement)
	s.Effect == "Allow"
	wildcard_principal(s.Principal)
	not restricted(s)
]

deny contains m if {
	some p in f.changed_of_type("aws_s3_bucket_policy")
	count(public_statements(p)) > 0
	not f.exempt("iacp.s3.no_public_policy", p.address)
	m := f.msg(
		"iacp.s3.no_public_policy", p.address,
		`policy allows Principal "*" without a restricting condition`,
		"scope the principal or add an aws:PrincipalOrgID / aws:SourceArn / aws:SourceAccount / aws:SourceVpce condition",
	)
}

warn contains m if {
	some p in f.changed_of_type("aws_s3_bucket_policy")
	f.policy_unknown(p, "policy")
	m := f.msg(
		"iacp.s3.policy_unknown", p.address,
		"bucket policy is only known after apply, so it was not checked for public access",
		"build it with data.aws_iam_policy_document (checked at plan time) and review it by hand",
	)
}
