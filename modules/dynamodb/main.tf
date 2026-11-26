resource "aws_dynamodb_table" "this" {
  name                        = local.full_name
  billing_mode                = var.billing_mode
  read_capacity               = local.is_provisioned ? var.read_capacity : null
  write_capacity              = local.is_provisioned ? var.write_capacity : null
  hash_key                    = var.hash_key
  range_key                   = var.range_key
  table_class                 = var.table_class
  deletion_protection_enabled = local.deletion_protection_enabled
  stream_enabled              = var.stream_view_type != null
  stream_view_type            = var.stream_view_type

  dynamic "attribute" {
    for_each = local.attributes

    content {
      name = attribute.key
      type = attribute.value
    }
  }

  dynamic "global_secondary_index" {
    for_each = var.global_secondary_indexes

    content {
      name               = global_secondary_index.value.name
      hash_key           = global_secondary_index.value.hash_key
      range_key          = global_secondary_index.value.range_key
      projection_type    = global_secondary_index.value.projection_type
      non_key_attributes = global_secondary_index.value.projection_type == "INCLUDE" ? global_secondary_index.value.non_key_attributes : null
      read_capacity      = local.is_provisioned ? coalesce(global_secondary_index.value.read_capacity, var.read_capacity) : null
      write_capacity     = local.is_provisioned ? coalesce(global_secondary_index.value.write_capacity, var.write_capacity) : null
    }
  }

  # Point-in-time recovery is always on; it is not an input.
  point_in_time_recovery {
    enabled = true
  }

  # Always a customer managed key, never the AWS owned or aws/dynamodb key.
  server_side_encryption {
    enabled     = true
    kms_key_arn = local.kms_key_arn
  }

  dynamic "ttl" {
    for_each = var.ttl_attribute_name == null ? [] : [var.ttl_attribute_name]

    content {
      enabled        = true
      attribute_name = ttl.value
    }
  }

  tags = local.tags
}
