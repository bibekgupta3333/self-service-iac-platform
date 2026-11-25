# METADATA
# title: conftest entrypoint
# description: |
#   Aggregates every iacp.* policy package into conftest's default `main`
#   namespace so `conftest test plan.json -p policies/opa` evaluates all rules.
#   Individual packages can still be selected with --namespace iacp.<name>.
package main

deny contains m if {
	some name
	some m in data.iacp[name].deny
}

warn contains m if {
	some name
	some m in data.iacp[name].warn
}
