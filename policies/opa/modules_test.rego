package iacp.modules_test

import data.iacp.modules
import data.lib.testing as t

repo := "git::https://github.com/bibekgupta3333/self-service-iac-platform.git"

src(version) := sprintf("%s//modules/s3?ref=modules/s3/v%s", [repo, version])

yanked_ctx := {"environment": "staging", "yanked_module_versions": [
	{"module": "s3", "version": "1.1.0", "tag": "modules/s3/v1.1.0"},
	{"module": "sqs", "version": "2.0.0", "tag": "modules/sqs/v2.0.0"},
]}

bucket_in(mod) := t.rc_in(mod, "aws_s3_bucket", "this", {"bucket": "b"})

plan_with(version, rcs) := object.union(t.plan(rcs), {"configuration": {"root_module": {"module_calls": {"store": {"source": src(version)}}}}})

test_yanked_version_with_changes_denied if {
	ms := modules.deny with input as plan_with("1.1.0", [bucket_in("store")]) with data.iacp.context as yanked_ctx
	count(ms) == 1
	some m in ms
	m == "[iacp.modules.yanked] module.store: modules/s3/v1.1.0 is yanked and this plan creates or updates its resources. Fix: pin the module to a release that is not yanked (the catalog lists the nearest safe version) and re-plan"
	count(modules.warn) == 0 with input as plan_with("1.1.0", [bucket_in("store")]) with data.iacp.context as yanked_ctx
}

test_nested_module_and_instance_keys_count_as_changes if {
	nested := object.union(bucket_in("store"), {"module_address": "module.store.module.kms"})
	keyed := object.union(bucket_in("store"), {"module_address": "module.store[0]"})
	count(modules.deny) == 1 with input as plan_with("1.1.0", [nested]) with data.iacp.context as yanked_ctx
	count(modules.deny) == 1 with input as plan_with("1.1.0", [keyed]) with data.iacp.context as yanked_ctx
}

test_other_module_prefix_is_not_a_change if {
	other := bucket_in("store_backup") # module.store_backup is not module.store
	count(modules.deny) == 0 with input as plan_with("1.1.0", [other]) with data.iacp.context as yanked_ctx
}

test_active_version_allowed if {
	count(modules.deny) == 0 with input as plan_with("1.2.0", [bucket_in("store")]) with data.iacp.context as yanked_ctx
	count(modules.warn) == 0 with input as plan_with("1.2.0", [bucket_in("store")]) with data.iacp.context as yanked_ctx
}

test_no_context_nothing_yanked if {
	count(modules.deny) == 0 with input as plan_with("1.1.0", [bucket_in("store")])
}

test_unchanged_yanked_module_only_warns if {
	noop := t.with_actions(bucket_in("store"), ["no-op"])
	count(modules.deny) == 0 with input as plan_with("1.1.0", [noop]) with data.iacp.context as yanked_ctx
	some w in modules.warn with input as plan_with("1.1.0", [noop]) with data.iacp.context as yanked_ctx
	startswith(w, "[iacp.modules.yanked_pinned] module.store: modules/s3/v1.1.0 is yanked")
}

test_destroying_yanked_module_only_warns if {
	gone := t.with_actions(bucket_in("store"), ["delete"])
	count(modules.deny) == 0 with input as plan_with("1.1.0", [gone]) with data.iacp.context as yanked_ctx
	count(modules.warn) == 1 with input as plan_with("1.1.0", [gone]) with data.iacp.context as yanked_ctx
}

test_unpinned_source_ignored_here if {
	unpinned := object.union(t.plan([bucket_in("store")]), {"configuration": {"root_module": {"module_calls": {"store": {"source": "../modules/s3"}}}}})
	count(modules.deny) == 0 with input as unpinned with data.iacp.context as yanked_ctx
}

test_exception_waives_yanked if {
	exc := {"exceptions": [{"rule_id": "iacp.modules.yanked", "resource_address": "module.store", "expires_at": "2200-01-01T00:00:00Z"}]}
	ctx := object.union(yanked_ctx, exc)
	count(modules.deny) == 0 with input as plan_with("1.1.0", [bucket_in("store")]) with data.iacp.context as ctx
}

test_prerelease_tags_parsed if {
	modules.pinned_tag(src("1.1.0-rc.1")) == "modules/s3/v1.1.0-rc.1"
	not modules.pinned_tag(concat("", [repo, "//modules/s3?ref=main"]))
}

test_main_aggregates_yanked if {
	some m in data.main.deny with input as plan_with("1.1.0", [bucket_in("store")]) with data.iacp.context as yanked_ctx
	startswith(m, "[iacp.modules.yanked]")
}
