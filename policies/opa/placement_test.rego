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

with_variables(inp, vars) := object.union(inp, {"variables": vars})

test_root_variable_region_resolved_and_allowed if {
	inp := with_variables(provider_input({"references": ["var.region"]}), {"region": {"value": "us-west-2"}})
	count(placement.deny) == 0 with input as inp
	count(placement.warn) == 0 with input as inp
}

test_root_variable_region_resolved_and_denied if {
	inp := with_variables(provider_input({"references": ["var.region"]}), {"region": {"value": "eu-central-1"}})
	some m in placement.deny with input as inp
	startswith(m, `[iacp.placement.region] provider.aws: region "eu-central-1" is not allowed`)
}

test_module_provider_variable_not_resolved_from_root if {
	inp := {
		"configuration": {"provider_config": {"module.x:aws": {"name": "aws", "expressions": {"region": {"references": ["var.region"]}}}}},
		"variables": {"region": {"value": "eu-central-1"}},
	}
	count(placement.deny) == 0 with input as inp
	count(placement.warn) == 1 with input as inp
}

test_unresolvable_region_warns_but_does_not_deny if {
	inp := provider_input({"references": ["local.region"]})
	count(placement.deny) == 0 with input as inp
	count(placement.warn) == 1 with input as inp
}

test_missing_region_denied if {
	inp := {"configuration": {"provider_config": {"aws": {"name": "aws", "expressions": {"access_key": {"constant_value": "test"}}}}}}
	some m in placement.deny with input as inp
	startswith(m, "[iacp.placement.region_missing] provider.aws: provider sets no region")
}

test_missing_expressions_denied if {
	count(placement.deny) == 1 with input as {"configuration": {"provider_config": {"aws": {"name": "aws"}}}}
}

test_constant_region_does_not_warn if {
	count(placement.warn) == 0 with input as provider_input({"constant_value": "us-east-1"})
}
