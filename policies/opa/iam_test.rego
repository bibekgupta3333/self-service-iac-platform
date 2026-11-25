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
	p := policy([allow(["s3:GetObject", "s3:PutObject"], "*")])
	some m in iam.deny with input as t.plan([p])
	contains(m, `[iacp.iam.wildcard_resource] aws_iam_role_policy.p: statement "S" grants write actions ["s3:putobject"]`)
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

test_unknown_policy_ignored if {
	p := t.with_unknown(t.rc("aws_iam_policy", "p", {}), {"policy": true})
	count(iam.deny) == 0 with input as t.plan([p])
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
