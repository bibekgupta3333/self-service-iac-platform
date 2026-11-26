# Two roles, both under /iacp/ (iam:PassRole for the platform is limited to
# that path) and both with a permissions boundary:
#   - the execution role is what ECS uses to start the task: pull from this
#     service's repository, write to its log group, and read exactly the
#     secrets listed in var.secrets;
#   - the task role is what the application code runs as. It has no access of
#     its own; var.extra_policy_jsons (outputs of other modules) grant it.

data "aws_iam_policy_document" "assume" {
  statement {
    sid     = "EcsTasksAssumeRole"
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:${data.aws_partition.current.partition}:ecs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:*"]
    }
  }
}

# ---- Execution role ----

resource "aws_iam_role" "execution" {
  name                 = "${local.full_name}-execution"
  path                 = "/iacp/"
  assume_role_policy   = data.aws_iam_policy_document.assume.json
  permissions_boundary = local.permissions_boundary_arn
  tags                 = local.tags
}

resource "aws_iam_role_policy" "execution" {
  name   = "execution"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution.json
}

data "aws_iam_policy_document" "execution" {
  #checkov:skip=CKV_AWS_356:Only ecr:GetAuthorizationToken uses resource "*"; the API does not support resource-level permissions.
  #checkov:skip=CKV_AWS_111:Only ecr:GetAuthorizationToken uses resource "*"; the API does not support resource-level permissions.
  statement {
    sid       = "EcrAuthorizationToken"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid       = "PullOwnRepository"
    effect    = "Allow"
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"]
    resources = [aws_ecr_repository.this.arn]
  }

  statement {
    sid       = "WriteOwnLogs"
    effect    = "Allow"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }

  dynamic "statement" {
    for_each = length(local.secretsmanager_arns) > 0 ? [local.secretsmanager_arns] : []

    content {
      sid       = "ReadInjectedSecrets"
      effect    = "Allow"
      actions   = ["secretsmanager:GetSecretValue"]
      resources = statement.value
    }
  }

  dynamic "statement" {
    for_each = length(local.ssm_parameter_arns) > 0 ? [local.ssm_parameter_arns] : []

    content {
      sid       = "ReadInjectedParameters"
      effect    = "Allow"
      actions   = ["ssm:GetParameters"]
      resources = statement.value
    }
  }

  dynamic "statement" {
    for_each = length(var.secrets_kms_key_arns) > 0 ? [var.secrets_kms_key_arns] : []

    content {
      sid       = "DecryptInjectedSecrets"
      effect    = "Allow"
      actions   = ["kms:Decrypt"]
      resources = statement.value
    }
  }
}

# ---- Task role ----

resource "aws_iam_role" "task" {
  name                 = "${local.full_name}-task"
  path                 = "/iacp/"
  assume_role_policy   = data.aws_iam_policy_document.assume.json
  permissions_boundary = local.permissions_boundary_arn
  tags                 = local.tags
}

# Keyed by caller-supplied names so adding or removing one document never
# renames or recreates the others.
resource "aws_iam_role_policy" "task_extra" {
  for_each = var.extra_policy_jsons

  name   = "extra-${each.key}"
  role   = aws_iam_role.task.id
  policy = each.value
}
