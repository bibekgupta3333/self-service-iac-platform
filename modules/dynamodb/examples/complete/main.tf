# Table with every option exercised: a composite key, a global secondary index
# with an INCLUDE projection, TTL and a stream. The key, PITR and deletion
# protection come from the module's secure defaults.

module "table" {
  source = "../../"

  name        = var.name
  team        = var.team
  environment = var.environment
  stack       = var.stack

  hash_key       = "pk"
  range_key      = "sk"
  range_key_type = "S"

  global_secondary_indexes = [
    {
      name               = "by-status"
      hash_key           = "status"
      range_key          = "created_at"
      range_key_type     = "N"
      projection_type    = "INCLUDE"
      non_key_attributes = ["order_total"]
    },
  ]

  ttl_attribute_name = "expires_at"
  stream_view_type   = "NEW_AND_OLD_IMAGES"

  tags = {
    "cost-center" = "cc-1234"
  }
}
