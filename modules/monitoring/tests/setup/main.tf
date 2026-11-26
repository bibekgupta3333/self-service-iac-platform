# Test fixture for modules/monitoring: an SQS queue to subscribe to the alarm
# topic. Built from the platform's own sqs module so it keeps the same secure
# defaults; SNS may use its key to deliver.

module "subscriber" {
  source = "../../../sqs"

  name                   = "monitoring-subscriber-${var.suffix}"
  team                   = "platform"
  environment            = "development"
  stack                  = "tftest"
  key_service_principals = ["sns.amazonaws.com"]
}
