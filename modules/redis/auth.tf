# auth_mode "token": a random AUTH token, stored in Secrets Manager under the
# module key. Applications read it from the secret (secret_arn output); it is
# never an output. Like any argument, it is also held in Terraform state,
# which the platform keeps encrypted and access-controlled (docs/02).
resource "random_password" "auth_token" {
  count = local.token_auth ? 1 : 0

  # ElastiCache accepts 16-128 printable characters except space, " / and @;
  # alphanumerics avoid quoting problems in client configuration.
  length  = 64
  special = false
}

resource "aws_secretsmanager_secret" "auth_token" {
  #checkov:skip=CKV2_AWS_57:Rotating an AUTH token needs a coordinated ROTATE/SET on the replication group; scheduled rotation is planned for a later minor release.
  count = local.token_auth ? 1 : 0

  name                    = "iacp/redis/${local.full_name}/auth-token"
  description             = "AUTH token of Redis ${local.full_name}"
  kms_key_id              = local.kms_key_arn
  recovery_window_in_days = local.is_development ? 0 : 7
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "auth_token" {
  count = local.token_auth ? 1 : 0

  secret_id     = aws_secretsmanager_secret.auth_token[0].id
  secret_string = random_password.auth_token[0].result
}
