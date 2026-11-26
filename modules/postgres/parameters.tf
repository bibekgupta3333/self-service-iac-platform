resource "aws_db_parameter_group" "this" {
  name        = "${local.full_name}-${local.parameter_family}"
  family      = local.parameter_family
  description = "PostgreSQL ${local.full_name}: TLS required, connection and DDL logging"
  tags        = local.tags

  # Reject every connection that does not use TLS.
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }

  parameter {
    name  = "log_connections"
    value = "1"
  }

  parameter {
    name  = "log_disconnections"
    value = "1"
  }

  parameter {
    name  = "log_statement"
    value = "ddl"
  }

  parameter {
    name  = "log_min_duration_statement"
    value = "1000"
  }

  # A new major version needs a new family; create it before the old one goes.
  lifecycle {
    create_before_destroy = true
  }
}
