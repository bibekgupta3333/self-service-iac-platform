package iacp.ecr_test

import data.iacp.ecr
import data.lib.testing as t

repo(after) := t.rc("aws_ecr_repository", "repo", object.union(
	{"image_scanning_configuration": [{"scan_on_push": true}], "image_tag_mutability": "IMMUTABLE"},
	after,
))

test_scanned_immutable_repository_allowed if {
	count(ecr.deny) == 0 with input as t.plan([repo({})])
}

test_repository_without_scan_on_push_denied if {
	some m in ecr.deny with input as t.plan([repo({"image_scanning_configuration": [{"scan_on_push": false}]})])
	m == concat("", [
		"[iacp.ecr.scan_on_push] aws_ecr_repository.repo: images are not scanned on push. ",
		"Fix: add image_scanning_configuration { scan_on_push = true }",
	])
}

test_repository_without_scanning_block_denied if {
	t.rule_ids(ecr.deny) == {"[iacp.ecr.scan_on_push]"} with input as t.plan([repo({"image_scanning_configuration": []})])
}

test_mutable_tags_denied if {
	some m in ecr.deny with input as t.plan([repo({"image_tag_mutability": "MUTABLE"})])
	startswith(m, "[iacp.ecr.immutable_tags] aws_ecr_repository.repo: image tags are MUTABLE")
}

test_missing_mutability_counts_as_mutable if {
	r := t.rc("aws_ecr_repository", "repo", {"image_scanning_configuration": [{"scan_on_push": true}]})
	t.rule_ids(ecr.deny) == {"[iacp.ecr.immutable_tags]"} with input as t.plan([r])
}

test_unchanged_repository_ignored if {
	r := t.with_actions(repo({"image_tag_mutability": "MUTABLE"}), ["no-op"])
	count(ecr.deny) == 0 with input as t.plan([r])
}
