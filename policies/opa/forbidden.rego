# METADATA
# title: Forbidden constructs
# description: |
#   Module calls must come from this repository pinned to an exact release tag
#   (?ref=modules/<name>/vX.Y.Z), force_destroy is only allowed in
#   development, and code-execution / exfiltration paths (data "external",
#   data "http", provisioners) are not allowed anywhere. None of these rules
#   can be waived.
package iacp.forbidden

import data.lib.iacp as f

# --- Configuration walk -------------------------------------------------------------

# config_blocks contains [address, block] for every resource and data block in the
# configuration, including nested modules (module.a.module.b.data.http.x).
config_blocks contains [address, r] if {
	walk(input.configuration.root_module, [path, resources])
	module_resources_path(path)
	some r in resources
	address := concat("", array.concat(module_prefixes(path), [r.address]))
}

# module_resources_path: ["resources"] or ["module_calls", n, "module", ..., "resources"].
module_resources_path(path) if {
	n := count(path)
	n % 3 == 1
	path[n - 1] == "resources"
	count({i |
		some i, e in path
		i < n - 1
		not module_step(i, e)
	}) == 0
}

module_step(i, "module_calls") if i % 3 == 0

module_step(i, _) if i % 3 == 1

module_step(i, "module") if i % 3 == 2

module_prefixes(path) := [sprintf("module.%s.", [path[i + 1]]) | some i, e in path; e == "module_calls"; i % 3 == 0]

forbidden_data_sources := {"external", "http"}

deny contains m if {
	some [address, r] in config_blocks
	r.mode == "data"
	r.type in forbidden_data_sources
	m := f.msg(
		"iacp.forbidden.data_source", address,
		sprintf("data %q can run programs or reach arbitrary URLs during plan", [r.type]),
		"remove it; pass values in as module inputs or use an AWS data source",
	)
}

deny contains m if {
	some [address, r] in config_blocks
	some p in r.provisioners
	m := f.msg(
		"iacp.forbidden.provisioner", address,
		sprintf("%s provisioner runs arbitrary commands during apply", [p.type]),
		"remove the provisioner; model the step as a resource or run it outside Terraform",
	)
}

source_pattern := `^git::https://(github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)\.git//modules/([a-z0-9-]+)\?ref=modules/([a-z0-9-]+)/v[0-9]+\.[0-9]+\.[0-9]+$`

# source_parts(src) := [repo, module dir, module in ref] when src matches.
source_parts(src) := array.slice(regex.find_all_string_submatch_n(source_pattern, src, 1)[0], 1, 4)

pinned_platform_source(src) if {
	[repo, dir, ref_name] := source_parts(src)
	repo in f.allowed_module_repos
	dir == ref_name
}

deny contains m if {
	some name, call in input.configuration.root_module.module_calls
	not pinned_platform_source(call.source)
	not f.exempt("iacp.forbidden.module_source", sprintf("module.%s", [name]))
	m := f.msg(
		"iacp.forbidden.module_source", sprintf("module.%s", [name]),
		sprintf("source %q is not a platform module pinned to a release tag", [call.source]),
		sprintf(
			"use git::https://%s.git//modules/<name>?ref=modules/<name>/vX.Y.Z (never a branch such as main)",
			[f.allowed_module_repos[0]],
		),
	)
}

deny contains m if {
	some rc in f.changed
	f.after(rc).force_destroy == true
	env := f.environment_of(rc)
	env != "development"
	not f.exempt("iacp.forbidden.force_destroy", rc.address)
	m := f.msg(
		"iacp.forbidden.force_destroy", rc.address,
		sprintf("force_destroy = true is not allowed in environment %q", [env]),
		"set force_destroy = false; empty the resource through the break-glass workflow if it must be destroyed",
	)
}
