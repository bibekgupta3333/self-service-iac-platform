# Bucket with every option exercised: lifecycle transition, access logging to
# a second bucket created with the same module, and extra user tags.

module "access_logs" {
  source = "../../"

  name          = "${var.name}-logs"
  team          = var.team
  environment   = var.environment
  stack         = var.stack
  force_destroy = true
}

module "bucket" {
  source = "../../"

  name                               = var.name
  team                               = var.team
  environment                        = var.environment
  stack                              = var.stack
  versioning_enabled                 = true
  noncurrent_version_expiration_days = 30
  transition_to_ia_days              = 90
  force_destroy                      = true
  access_log_bucket                  = module.access_logs.id

  tags = {
    "cost-center" = "cc-1234"
    "purpose"     = "example"
  }
}
