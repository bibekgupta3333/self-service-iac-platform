# The alarm topic is the module's primary resource. It is always encrypted
# with a customer managed key: the AWS-managed alias/aws/sns key cannot be
# granted to CloudWatch, so alarms published to such a topic never notify.
resource "aws_sns_topic" "this" {
  name              = local.full_name
  kms_master_key_id = local.kms_key_arn
  tags              = local.tags
}
