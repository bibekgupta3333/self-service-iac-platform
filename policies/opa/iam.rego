# METADATA
# title: Least-privilege IAM
# description: |
#   IAM policy documents must not allow every action ("*", "<svc>:*" on
#   Resource "*", or NotAction) and must not grant write actions on
#   Resource "*".
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

top_level_documents(rc) := {json.unmarshal(doc) |
	rc.type in policy_types
	doc := f.after(rc).policy
	is_string(doc)
}

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
