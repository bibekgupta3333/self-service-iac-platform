# METADATA
# title: Module versions
# description: |
#   A module call pinned to a yanked release (listed by the platform API in
#   data.iacp.context.yanked_module_versions, served by
#   GET /v1/internal/budget-context) must not create or update resources.
#   Plans that leave the yanked module untouched, and destroys, still pass
#   (docs/06 §2: the stack is flagged and an upgrade request is opened) and
#   only get a warning. Without the context nothing is listed as yanked.
package iacp.modules

import data.lib.iacp as f

# yanked_tags: "modules/<name>/vX.Y.Z" for every yanked version in the context.
yanked_tags contains sprintf("modules/%s/v%s", [y.module, y.version]) if {
	some y in f.ctx_value("yanked_module_versions", [])
}

ref_pattern := `\?ref=(modules/[a-z0-9-]+/v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)$`

# pinned_tag(src): the release tag a module source is pinned to.
pinned_tag(src) := regex.find_all_string_submatch_n(ref_pattern, src, 1)[0][1]

# yanked_calls contains [module address, tag] for root module calls pinned to a yanked tag.
yanked_calls contains [sprintf("module.%s", [name]), tag] if {
	some name, call in input.configuration.root_module.module_calls
	tag := pinned_tag(call.source)
	tag in yanked_tags
}

# writes_module(addr): the plan creates or updates a resource inside module addr
# (including nested modules and instance keys).
writes_module(addr) if {
	some rc in f.changed
	in_module(f.module_of(rc), addr)
}

in_module(m, addr) if m == addr

in_module(m, addr) if startswith(m, concat("", [addr, "."]))

in_module(m, addr) if startswith(m, concat("", [addr, "["]))

deny contains m if {
	some [addr, tag] in yanked_calls
	writes_module(addr)
	not f.exempt("iacp.modules.yanked", addr)
	m := f.msg(
		"iacp.modules.yanked", addr,
		sprintf("%s is yanked and this plan creates or updates its resources", [tag]),
		"pin the module to a release that is not yanked (the catalog lists the nearest safe version) and re-plan",
	)
}

warn contains m if {
	some [addr, tag] in yanked_calls
	not writes_module(addr)
	m := f.msg(
		"iacp.modules.yanked_pinned", addr,
		sprintf("%s is yanked; this plan does not change it, but the stack should be upgraded", [tag]),
		"merge the platform's upgrade request or pin a release that is not yanked",
	)
}
