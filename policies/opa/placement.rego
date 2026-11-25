# METADATA
# title: Allowed regions
# description: |
#   AWS providers must target an allowed region. The allow-list comes from
#   data.iacp.context.allowed_regions (default us-east-1, us-west-2).
package iacp.placement

import data.lib.iacp as f

aws_providers contains [key, p] if {
	some key, p in input.configuration.provider_config
	p.name == "aws"
}

deny contains m if {
	some [key, p] in aws_providers
	region := p.expressions.region.constant_value
	not region in f.allowed_regions
	not f.exempt("iacp.placement.region", sprintf("provider.%s", [key]))
	m := f.msg(
		"iacp.placement.region", sprintf("provider.%s", [key]),
		sprintf("region %q is not allowed", [region]),
		sprintf("use one of %v", [f.allowed_regions]),
	)
}

# A region that is not a literal cannot be verified from the plan.
warn contains m if {
	some [key, p] in aws_providers
	region := p.expressions.region
	not region.constant_value
	m := f.msg(
		"iacp.placement.region_unverified", sprintf("provider.%s", [key]),
		"region is not a constant and cannot be checked",
		"set region to a literal in providers.tf",
	)
}
