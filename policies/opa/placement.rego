# METADATA
# title: Allowed regions
# description: |
#   AWS providers must set an allowed region. The allow-list comes from
#   data.iacp.context.allowed_regions (default us-east-1, us-west-2).
package iacp.placement

import data.lib.iacp as f

aws_providers contains [key, p] if {
	some key, p in input.configuration.provider_config
	p.name == "aws"
}

address(key) := sprintf("provider.%s", [key])

# region_of(key, p): a literal region, or a root variable resolved from input.variables.
region_of(_, p) := p.expressions.region.constant_value

region_of(key, p) := input.variables[name].value if {
	not contains(key, ":") # root-module provider: var.* refers to root variables
	refs := p.expressions.region.references
	count(refs) == 1
	ref := refs[0]
	startswith(ref, "var.")
	name := substring(ref, 4, -1)
}

has_region(p) if p.expressions.region

deny contains m if {
	some [key, p] in aws_providers
	region := region_of(key, p)
	not region in f.allowed_regions
	not f.exempt("iacp.placement.region", address(key))
	m := f.msg(
		"iacp.placement.region", address(key),
		sprintf("region %q is not allowed", [region]),
		sprintf("use one of %v", [f.allowed_regions]),
	)
}

# Without a region the provider falls back to AWS_REGION or a profile, which the
# plan cannot show.
deny contains m if {
	some [key, p] in aws_providers
	not has_region(p)
	not f.exempt("iacp.placement.region", address(key))
	m := f.msg(
		"iacp.placement.region_missing", address(key),
		"provider sets no region (it would come from the environment)",
		sprintf("set region to one of %v in providers.tf", [f.allowed_regions]),
	)
}

# A region expression that cannot be resolved from the plan.
warn contains m if {
	some [key, p] in aws_providers
	has_region(p)
	not region_of(key, p)
	m := f.msg(
		"iacp.placement.region_unverified", address(key),
		"region is neither a literal nor a root variable and cannot be checked",
		"set region to a literal or a root variable in providers.tf",
	)
}
