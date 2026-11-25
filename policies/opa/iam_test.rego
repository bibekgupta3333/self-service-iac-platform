package iacp.iam_test

import data.iacp.iam
import data.lib.testing as t

policy(statements) := t.rc("aws_iam_role_policy", "p", {"policy": json.marshal({"Version": "2012-10-17", "Statement": statements})})

allow(action, resource) := {"Sid": "S", "Effect": "Allow", "Action": action, "Resource": resource}

test_scoped_policy_allowed if {
	p := policy([allow(["sqs:SendMessage", "sqs:GetQueueUrl"], "arn:aws:sqs:us-east-1:111111111111:orders")])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_read_only_on_all_resources_allowed if {
	p := policy([allow(["s3:ListAllMyBuckets", "ec2:Describe*"], "*")])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_resource_star_only_actions_allowed if {
	p := policy([allow(["xray:PutTraceSegments", "xray:PutTelemetryRecords"], "*")])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_deny_statement_with_wildcards_allowed if {
	p := policy([{"Effect": "Deny", "Action": "*", "Resource": "*"}])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_wildcard_action_denied if {
	p := policy([allow("*", "arn:aws:s3:::orders/*")])
	some m in iam.deny with input as t.plan([p])
	m == `[iacp.iam.wildcard_action] aws_iam_role_policy.p: statement "S" allows Action "*". Fix: list the specific actions the workload needs`
}

test_star_colon_star_denied if {
	p := policy([allow(["*:*"], "arn:aws:s3:::orders/*")])
	count(iam.deny) == 1 with input as t.plan([p])
}

test_not_action_denied if {
	p := policy([{"Effect": "Allow", "NotAction": "iam:*", "Resource": "arn:aws:s3:::orders/*"}])
	some m in iam.deny with input as t.plan([p])
	contains(m, "uses Allow with NotAction")
}

test_write_on_all_resources_denied if {
	p := policy([allow(["s3:ListBucket", "s3:PutObject"], "*")])
	some m in iam.deny with input as t.plan([p])
	contains(m, `[iacp.iam.wildcard_resource] aws_iam_role_policy.p: statement "S" grants write or sensitive actions ["s3:putobject"] on all resources`)
}

test_sensitive_reads_on_all_resources_denied if {
	p := policy([allow(["secretsmanager:GetSecretValue", "ssm:GetParametersByPath", "s3:GetObject", "s3:Get*"], "*")])
	some m in iam.deny with input as t.plan([p])
	contains(m, `["s3:get*", "s3:getobject", "secretsmanager:getsecretvalue", "ssm:getparametersbypath"]`)
}

test_sensitive_reads_on_scoped_resources_allowed if {
	p := policy([allow(["secretsmanager:GetSecretValue"], "arn:aws:secretsmanager:us-east-1:111111111111:secret:orders-*")])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_account_wide_arn_wildcard_treated_as_all_resources if {
	p := policy([allow("s3:PutObject", "arn:aws:s3:::*")])
	t.rule_ids(iam.deny) == {"[iacp.iam.wildcard_resource]"} with input as t.plan([p])
}

test_arn_with_scoped_prefix_not_wildcard if {
	p := policy([allow("s3:PutObject", "arn:aws:s3:::orders-*")])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_not_resource_denied if {
	p := policy([{"Sid": "S", "Effect": "Allow", "Action": "s3:GetObject", "NotResource": "arn:aws:s3:::secret/*"}])
	some m in iam.deny with input as t.plan([p])
	startswith(m, `[iacp.iam.not_resource] aws_iam_role_policy.p: statement "S" uses Allow with NotResource`)
}

test_deny_with_not_resource_allowed if {
	p := policy([{"Effect": "Deny", "Action": "s3:*", "NotResource": "arn:aws:s3:::orders/*"}])
	count(iam.deny) == 0 with input as t.plan([p])
}

test_service_wildcard_on_all_resources_denied if {
	p := policy({"Effect": "Allow", "Action": "sqs:*", "Resource": ["*"]})
	some m in iam.deny with input as t.plan([p])
	contains(m, `statement "<no sid>"`)
}

test_role_inline_policy_checked if {
	doc := json.marshal({"Statement": [allow("*", "*")]})
	r := t.rc("aws_iam_role", "r", {"inline_policy": [{"name": "x", "policy": doc}]})
	count(iam.deny) == 2 with input as t.plan([r])
}

# jsonencode({Action = "*", Resource = aws_sqs_queue.q.arn}): nothing to read at plan time.
test_computed_jsonencode_policy_warns if {
	p := t.computed_policy("aws_iam_role_policy", "p")
	count(iam.deny) == 0 with input as t.plan([p])
	iam.warn == {concat("", [
		"[iacp.iam.policy_unknown] aws_iam_role_policy.p: policy document is only known after apply, so it was not checked. ",
		"Fix: build it with data.aws_iam_policy_document (checked at plan time) and review it by hand",
	])} with input as t.plan([p])
}

test_known_policy_does_not_warn if {
	p := policy([allow("sqs:SendMessage", "arn:aws:sqs:us-east-1:111111111111:q")])
	count(iam.warn) == 0 with input as t.plan([p])
}

# data.aws_iam_policy_document with Action "*" and a computed Resource.
test_policy_document_wildcard_action_with_computed_resource_denied if {
	p := t.computed_policy("aws_iam_role_policy", "p")
	d := t.policy_doc_data("d", [{"effect": "Allow", "actions": ["*"], "resources": [null], "principals": [], "condition": []}])
	inp := t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
	t.rule_ids(iam.deny) == {"[iacp.iam.wildcard_action]"} with input as inp
	count(iam.warn) == 0 with input as inp
}

test_policy_document_scoped_actions_allowed if {
	p := t.computed_policy("aws_iam_policy", "p")
	d := t.policy_doc_data("d", [{"actions": ["sqs:SendMessage"], "resources": [null]}])
	count(iam.deny) == 0 with input as t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
}

test_policy_document_of_other_resource_not_used if {
	p := t.computed_policy("aws_iam_policy", "p")
	d := t.policy_doc_data("other", [{"actions": ["*"], "resources": [null]}])
	inp := t.plan_with_config([p, d], [t.cfg(p.type, "p", t.policy_ref("d"))])
	count(iam.deny) == 0 with input as inp
	count(iam.warn) == 1 with input as inp
}

test_constant_policy_in_configuration_checked if {
	p := t.computed_policy("aws_iam_policy", "p")
	doc := json.marshal({"Statement": [allow("*", "arn:aws:s3:::b")]})
	inp := t.plan_with_config([p], [t.cfg(p.type, "p", {"policy": {"constant_value": doc}})])
	t.rule_ids(iam.deny) == {"[iacp.iam.wildcard_action]"} with input as inp
}

test_exception_suppresses_wildcard_resource if {
	ctx := {"now": "2026-01-01T00:00:00Z", "exceptions": [{
		"rule_id": "iacp.iam.wildcard_resource",
		"resource_address": "aws_iam_role_policy.p",
		"expires_at": "2026-02-01T00:00:00Z",
	}]}
	p := policy([allow("s3:PutObject", "*")])
	count(iam.deny) == 0 with input as t.plan([p]) with data.iacp.context as ctx
}

# --- Managed policy attachments ----------------------------------------------------

attachment(type, arn) := t.rc(type, "a", {"policy_arn": arn})

test_admin_attachment_denied_for_every_attachment_type if {
	every type in iam.attachment_types {
		some m in iam.deny with input as t.plan([attachment(type, "arn:aws:iam::aws:policy/AdministratorAccess")])
		startswith(m, sprintf("[iacp.iam.admin_policy] %s.a: attaches the AWS-managed policy AdministratorAccess", [type]))
	}
}

test_power_user_and_iam_full_access_denied if {
	count(iam.deny) == 1 with input as t.plan([attachment("aws_iam_role_policy_attachment", "arn:aws:iam::aws:policy/PowerUserAccess")])
	count(iam.deny) == 1 with input as t.plan([attachment("aws_iam_user_policy_attachment", "arn:aws:iam::aws:policy/IAMFullAccess")])
}

test_gov_partition_admin_attachment_denied if {
	count(iam.deny) == 1 with input as t.plan([attachment("aws_iam_role_policy_attachment", "arn:aws-us-gov:iam::aws:policy/AdministratorAccess")])
}

test_role_managed_policy_arns_checked if {
	r := t.rc("aws_iam_role", "r", {"managed_policy_arns": ["arn:aws:iam::aws:policy/AdministratorAccess"]})
	count(iam.deny) == 1 with input as t.plan([r])
}

test_full_access_attachment_warns if {
	inp := t.plan([attachment("aws_iam_role_policy_attachment", "arn:aws:iam::aws:policy/AmazonS3FullAccess")])
	count(iam.deny) == 0 with input as inp
	t.rule_ids(iam.warn) == {"[iacp.iam.full_access_policy]"} with input as inp
}

test_scoped_managed_policy_allowed if {
	inp := t.plan([attachment("aws_iam_role_policy_attachment", "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole")])
	count(iam.deny) == 0 with input as inp
	count(iam.warn) == 0 with input as inp
}

test_customer_policy_named_like_admin_allowed if {
	count(iam.deny) == 0 with input as t.plan([attachment("aws_iam_role_policy_attachment", "arn:aws:iam::111111111111:policy/AdministratorAccess")])
}

test_unknown_policy_arn_ignored if {
	r := t.with_unknown(t.rc("aws_iam_role_policy_attachment", "a", {}), {"policy_arn": true})
	count(iam.deny) == 0 with input as t.plan([r])
}
