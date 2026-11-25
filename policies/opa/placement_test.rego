package iacp.placement_test

import data.iacp.placement

provider_input(region_expr) := {"configuration": {"provider_config": {"aws": {"name": "aws", "expressions": {"region": region_expr}}}}}

test_allowed_region_passes if {
	count(placement.deny) == 0 with input as provider_input({"constant_value": "us-east-1"})
}

test_disallowed_region_denied if {
	some m in placement.deny with input as provider_input({"constant_value": "ap-south-1"})
	m == `[iacp.placement.region] provider.aws: region "ap-south-1" is not allowed. Fix: use one of ["us-east-1", "us-west-2"]`
}

test_context_allow_list_used if {
	count(placement.deny) == 0 with input as provider_input({"constant_value": "eu-west-1"})
		with data.iacp.context as {"allowed_regions": ["eu-west-1"]}
}

test_non_aws_provider_ignored if {
	inp := {"configuration": {"provider_config": {"random": {"name": "random", "expressions": {"region": {"constant_value": "mars-1"}}}}}}
	count(placement.deny) == 0 with input as inp
}

test_variable_region_warns_but_does_not_deny if {
	inp := provider_input({"references": ["var.region"]})
	count(placement.deny) == 0 with input as inp
	count(placement.warn) == 1 with input as inp
}

test_constant_region_does_not_warn if {
	count(placement.warn) == 0 with input as provider_input({"constant_value": "us-east-1"})
}
