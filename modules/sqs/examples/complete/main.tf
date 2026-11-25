# Queue with every option exercised: custom timings, redrive count and alarm
# notifications to an encrypted SNS topic.

resource "aws_sns_topic" "alarms" {
  name              = "${var.team}-${var.environment}-${var.name}-alarms"
  kms_master_key_id = "alias/aws/sns"

  tags = {
    "iacp:team"        = var.team
    "iacp:environment" = var.environment
    "iacp:stack"       = var.stack
    "iacp:managed-by"  = "iacp"
  }
}

module "queue" {
  source = "../../"

  name                             = var.name
  team                             = var.team
  environment                      = var.environment
  stack                            = var.stack
  visibility_timeout_seconds       = 60
  message_retention_seconds        = 86400
  max_receive_count                = 3
  dlq_retention_seconds            = 1209600
  alarm_actions                    = [aws_sns_topic.alarms.arn]
  oldest_message_age_alarm_seconds = 900

  tags = {
    "cost-center" = "cc-1234"
  }
}
