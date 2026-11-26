# VPC flow logs to a KMS-encrypted CloudWatch log group, delivered by a role
# that may only write to that log group.

resource "aws_cloudwatch_log_group" "flow_logs" {
  #checkov:skip=CKV_AWS_338:Retention is an input (default 30 days); one-year retention is a per-stack compliance choice.
  name              = local.flow_log_group
  retention_in_days = var.flow_log_retention_days
  kms_key_id        = local.kms_key_arn
  tags              = local.tags
}

resource "aws_flow_log" "this" {
  vpc_id          = aws_vpc.this.id
  traffic_type    = var.flow_log_traffic_type
  log_destination = aws_cloudwatch_log_group.flow_logs.arn
  iam_role_arn    = aws_iam_role.flow_logs.arn

  tags = merge(local.tags, { Name = local.full_name })
}

resource "aws_iam_role" "flow_logs" {
  name                 = "${local.full_name}-flow-logs"
  path                 = "/iacp/"
  assume_role_policy   = data.aws_iam_policy_document.flow_logs_assume.json
  permissions_boundary = local.permissions_boundary_arn
  tags                 = local.tags
}

resource "aws_iam_role_policy" "flow_logs" {
  name   = "deliver-flow-logs"
  role   = aws_iam_role.flow_logs.id
  policy = data.aws_iam_policy_document.flow_logs.json
}

# Only the flow log service, acting for this account's flow logs, may assume
# the role (confused-deputy protection).
data "aws_iam_policy_document" "flow_logs_assume" {
  statement {
    sid     = "VpcFlowLogsForThisAccount"
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["vpc-flow-logs.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:${data.aws_partition.current.partition}:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:vpc-flow-log/*"]
    }
  }
}

data "aws_iam_policy_document" "flow_logs" {
  statement {
    sid       = "WriteFlowLogGroup"
    effect    = "Allow"
    actions   = ["logs:CreateLogStream", "logs:DescribeLogStreams", "logs:PutLogEvents"]
    resources = [aws_cloudwatch_log_group.flow_logs.arn, "${aws_cloudwatch_log_group.flow_logs.arn}:log-stream:*"]
  }
}
