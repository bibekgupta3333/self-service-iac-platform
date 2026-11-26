resource "aws_db_instance" "this" {
  #checkov:skip=CKV_AWS_118:Enhanced Monitoring needs a monitoring role; Performance Insights and exported logs cover 1.0.0, and the role is planned for a later minor release.
  #checkov:skip=CKV_AWS_293:Deletion protection is on in staging and production; development stays disposable so TTL destroys work (asserted in tests/postgres.tftest.hcl).
  #checkov:skip=CKV_AWS_133:Backups are always on: retention is 7 days (35 in production) or an input validated to 1-35 (at least 7 in production); Checkov cannot evaluate the local.
  identifier     = local.full_name
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class
  port           = var.port
  db_name        = var.database_name

  # Storage: gp3, always encrypted with a customer-managed key.
  storage_type          = "gp3"
  allocated_storage     = var.allocated_storage_gb
  max_allocated_storage = var.max_allocated_storage_gb
  storage_encrypted     = true
  kms_key_id            = local.kms_key_arn

  # Credentials: RDS generates and rotates the master password in Secrets
  # Manager, encrypted with the same key. Terraform never sees it.
  username                            = var.master_username
  manage_master_user_password         = true
  master_user_secret_kms_key_id       = local.kms_key_arn
  iam_database_authentication_enabled = var.iam_database_authentication_enabled

  # Network: private subnets, never public, reachable only through the group.
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  publicly_accessible    = false
  multi_az               = local.multi_az
  parameter_group_name   = aws_db_parameter_group.this.name

  # Recovery.
  backup_retention_period   = local.backup_retention_days
  copy_tags_to_snapshot     = true
  deletion_protection       = local.deletion_protection
  skip_final_snapshot       = local.skip_final_snapshot
  final_snapshot_identifier = local.skip_final_snapshot ? null : "${local.full_name}-final-${random_id.final_snapshot[0].hex}"
  delete_automated_backups  = local.is_development

  # Maintenance.
  auto_minor_version_upgrade  = true
  allow_major_version_upgrade = false
  apply_immediately           = false

  # Observability.
  performance_insights_enabled          = true
  performance_insights_kms_key_id       = local.kms_key_arn
  performance_insights_retention_period = 7
  enabled_cloudwatch_logs_exports       = local.log_exports

  tags = local.tags

  # The log groups must exist (with retention and KMS) before RDS exports to them.
  depends_on = [aws_cloudwatch_log_group.this]
}
