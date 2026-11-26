# METADATA
# title: Container image registry
# description: |
#   ECR repositories must scan every pushed image and keep tags immutable,
#   so a deployed tag always names the same, scanned image.
package iacp.ecr

import data.lib.iacp as f

scans_on_push(repo) if {
	some c in f.after(repo).image_scanning_configuration
	f.enabled(c.scan_on_push)
}

deny contains m if {
	some repo in f.changed_of_type("aws_ecr_repository")
	not scans_on_push(repo)
	not f.exempt("iacp.ecr.scan_on_push", repo.address)
	m := f.msg(
		"iacp.ecr.scan_on_push", repo.address,
		"images are not scanned on push",
		"add image_scanning_configuration { scan_on_push = true }",
	)
}

# The provider default is MUTABLE, so a missing value counts as mutable.
mutability(repo) := object.get(f.after(repo), "image_tag_mutability", "MUTABLE")

deny contains m if {
	some repo in f.changed_of_type("aws_ecr_repository")
	mutability(repo) != "IMMUTABLE"
	not f.exempt("iacp.ecr.immutable_tags", repo.address)
	m := f.msg(
		"iacp.ecr.immutable_tags", repo.address,
		sprintf("image tags are %v, so a deployed tag can be overwritten", [mutability(repo)]),
		`set image_tag_mutability = "IMMUTABLE"`,
	)
}
