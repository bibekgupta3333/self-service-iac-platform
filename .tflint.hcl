# TFLint configuration for the Self-Service IaC Platform.
#
# Used for every Terraform root and module in this repository:
#   tflint --init --config "$(git rev-parse --show-toplevel)/.tflint.hcl"
#   tflint --recursive --config "$(git rev-parse --show-toplevel)/.tflint.hcl"
#
# Plugin versions are pinned; bump them deliberately in a dedicated PR.

config {
  # Lint local module calls too, so problems inside modules surface at the caller.
  call_module_type = "local"
}

plugin "terraform" {
  enabled = true
  preset  = "recommended"
  version = "0.17.0"
  source  = "github.com/terraform-linters/tflint-ruleset-terraform"
}

plugin "aws" {
  enabled = true
  version = "0.50.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}

# --- Terraform language rules (on top of the recommended preset) -------------

# Every variable and output carries a description (AGENTS.md coding conventions).
rule "terraform_documented_variables" {
  enabled = true
}

rule "terraform_documented_outputs" {
  enabled = true
}

# Every variable declares a type.
rule "terraform_typed_variables" {
  enabled = true
}

# snake_case for every block label.
rule "terraform_naming_convention" {
  enabled = true
  format  = "snake_case"
}

# main.tf / variables.tf / outputs.tf layout.
rule "terraform_standard_module_structure" {
  enabled = true
}

# Providers and Terraform itself are pinned in versions.tf.
rule "terraform_required_providers" {
  enabled = true
}

rule "terraform_required_version" {
  enabled = true
}

# Module sources must be pinned to a ref (never a default branch). The exact
# ?ref=modules/<name>/vX.Y.Z format is enforced on the plan by OPA
# (iacp.forbidden.module_source); "semver" would reject that tag format.
rule "terraform_module_pinned_source" {
  enabled = true
  style   = "flexible"
}

rule "terraform_unused_declarations" {
  enabled = true
}

rule "terraform_comment_syntax" {
  enabled = true
}

rule "terraform_deprecated_index" {
  enabled = true
}

rule "terraform_unused_required_providers" {
  enabled = true
}

# --- AWS rules ----------------------------------------------------------------

# The iacp:* tag contract is applied through provider default_tags and enforced
# by OPA on the plan (policies/opa/tags.rego), where tags_all is known.
rule "aws_resource_missing_tags" {
  enabled = false
}
