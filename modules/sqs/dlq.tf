# The dead-letter queue is always created; it is a mandatory companion of the
# main queue, not an option.
resource "aws_sqs_queue" "dlq" {
  name                              = local.dlq_name
  fifo_queue                        = var.fifo
  message_retention_seconds         = var.dlq_retention_seconds
  receive_wait_time_seconds         = var.receive_wait_time_seconds
  kms_master_key_id                 = local.kms_key_arn
  kms_data_key_reuse_period_seconds = var.kms_data_key_reuse_period_seconds

  # Only the main queue may redrive into this DLQ. The main queue ARN is built
  # from its name because the main queue's redrive policy already depends on
  # this queue; referencing aws_sqs_queue.this here would create a cycle.
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [local.queue_arn]
  })

  tags = merge(local.tags, { "iacp:role" = "dlq" })
}
