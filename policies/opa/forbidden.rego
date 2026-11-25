# METADATA
# title: Forbidden constructs
# description: |
#   Module calls must come from this repository pinned to an exact release tag
#   (?ref=modules/<name>/vX.Y.Z), and force_destroy is only allowed in
#   development.
package iacp.forbidden

import data.lib.iacp as f

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
