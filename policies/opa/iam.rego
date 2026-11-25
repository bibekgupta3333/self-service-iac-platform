# METADATA
# title: Least-privilege IAM
# description: |
#   IAM policy documents must not allow every action ("*", "<svc>:*" on
#   Resource "*", or NotAction) and must not grant write actions on
#   Resource "*". AWS-managed admin policies must not be attached.
package iacp.iam

import data.lib.iacp as f

# Resources whose `policy` attribute is a JSON IAM policy document.
policy_types := {"aws_iam_policy", "aws_iam_role_policy", "aws_iam_user_policy", "aws_iam_group_policy"}

# Actions that only support Resource "*" (AWS service limitation).
resource_star_only := {
	"xray:puttracesegments",
	"xray:puttelemetryrecords",
	"cloudwatch:putmetricdata",
	"ec2:createnetworkinterface",
	"ec2:deletenetworkinterface",
	"ec2:assignprivateipaddresses",
	"ec2:unassignprivateipaddresses",
}

# documents(rc): every JSON policy document carried by a resource.
documents(rc) := top_level_documents(rc) | inline_documents(rc)

top_level_documents(rc) := f.policy_documents(rc, "policy") if rc.type in policy_types

top_level_documents(rc) := set() if not rc.type in policy_types

inline_documents(rc) := {json.unmarshal(inline.policy) |
	rc.type == "aws_iam_role"
	some inline in f.after(rc).inline_policy
	is_string(inline.policy)
}

allow_statements(rc) := [s |
	some doc in documents(rc)
	some s in f.as_array(doc.Statement)
	s.Effect == "Allow"
]

actions(s) := [lower(a) | some a in f.as_array(object.get(s, "Action", []))]

full_wildcard(action) if action in {"*", "*:*"}

read_only(action) if regex.match(`^[a-z0-9-]+:(get|list|describe|head|batchget|query|scan|search|lookup|view)`, action)

write_action(action) if {
	not read_only(action)
	not action in resource_star_only
}

all_resources(s) if "*" in f.as_array(object.get(s, "Resource", []))

# --- Rules -----------------------------------------------------------------------

deny contains m if {
	some rc in f.changed
	some s in allow_statements(rc)
	some a in actions(s)
	full_wildcard(a)
	not f.exempt("iacp.iam.wildcard_action", rc.address)
	m := f.msg(
		"iacp.iam.wildcard_action", rc.address,
		sprintf("statement %q allows Action %q", [object.get(s, "Sid", "<no sid>"), a]),
		"list the specific actions the workload needs",
	)
}

deny contains m if {
	some rc in f.changed
	some s in allow_statements(rc)
	s.NotAction
	not f.exempt("iacp.iam.wildcard_action", rc.address)
	m := f.msg(
		"iacp.iam.wildcard_action", rc.address,
		sprintf("statement %q uses Allow with NotAction", [object.get(s, "Sid", "<no sid>")]),
		"replace NotAction with an explicit Action list",
	)
}

deny contains m if {
	some rc in f.changed
	some s in allow_statements(rc)
	all_resources(s)
	writes := sort({a | some a in actions(s); write_action(a)})
	count(writes) > 0
	not f.exempt("iacp.iam.wildcard_resource", rc.address)
	m := f.msg(
		"iacp.iam.wildcard_resource", rc.address,
		sprintf("statement %q grants write actions %v on Resource \"*\"", [object.get(s, "Sid", "<no sid>"), writes]),
		"scope Resource to the ARNs the workload owns",
	)
}

# A document computed at apply time (e.g. jsonencode() with a computed Resource) cannot be
# checked from the plan; surface it instead of passing silently.
warn contains m if {
	some rc in f.changed
	rc.type in policy_types
	f.policy_unknown(rc, "policy")
	m := f.msg(
		"iacp.iam.policy_unknown", rc.address,
		"policy document is only known after apply, so it was not checked",
		"build it with data.aws_iam_policy_document (checked at plan time) and review it by hand",
	)
}

# --- AWS-managed policy attachments ------------------------------------------------

attachment_types := {
	"aws_iam_role_policy_attachment",
	"aws_iam_policy_attachment",
	"aws_iam_user_policy_attachment",
	"aws_iam_group_policy_attachment",
}

admin_managed_policies := {"AdministratorAccess", "PowerUserAccess", "IAMFullAccess"}

# attached_policy_arns(rc): managed policy ARNs a resource attaches.
attached_policy_arns(rc) := {arn | arn := f.after(rc).policy_arn; is_string(arn)} if rc.type in attachment_types

attached_policy_arns(rc) := {arn | some arn in f.after(rc).managed_policy_arns} if rc.type == "aws_iam_role"

# aws_managed_name(arn): "AdministratorAccess" for arn:<partition>:iam::aws:policy/[path/]AdministratorAccess.
aws_managed_name(arn) := regex.find_all_string_submatch_n(`^arn:aws[a-z-]*:iam::aws:policy/(?:.*/)?([A-Za-z0-9+=,.@_-]+)$`, arn, 1)[0][1]

deny contains m if {
	some rc in f.changed
	some arn in attached_policy_arns(rc)
	aws_managed_name(arn) in admin_managed_policies
	not f.exempt("iacp.iam.admin_policy", rc.address)
	m := f.msg(
		"iacp.iam.admin_policy", rc.address,
		sprintf("attaches the AWS-managed policy %s", [aws_managed_name(arn)]),
		"attach a customer-managed policy scoped to the workload's resources",
	)
}

warn contains m if {
	some rc in f.changed
	some arn in attached_policy_arns(rc)
	name := aws_managed_name(arn)
	endswith(name, "FullAccess")
	not name in admin_managed_policies
	m := f.msg(
		"iacp.iam.full_access_policy", rc.address,
		sprintf("attaches the broad AWS-managed policy %s", [name]),
		"prefer a customer-managed policy limited to the actions and resources the workload needs",
	)
}
