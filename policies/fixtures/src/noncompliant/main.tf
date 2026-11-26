# Non-compliant stack: breaks every OPA rule at least once (see ../../verify.sh for
# the expected rule IDs). The plan is generated offline with dummy credentials
# (see ../../regenerate.sh); nothing is applied.

terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.0.0"
    }
    http = {
      source  = "hashicorp/http"
      version = "3.5.0"
    }
  }
}

locals {
  secondary_region = "us-east-1"
}

# iacp.placement.region: not an allowed region.
provider "aws" {
  region                      = "eu-central-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  # iacp.tags.required (no stack / managed-by) and iacp.tags.mismatch (team).
  default_tags {
    tags = {
      "iacp:team"        = "search"
      "iacp:environment" = "staging"
    }
  }
}

# iacp.placement.region_missing (declared only so the policy can see it).
# tflint-ignore: terraform_unused_declarations
provider "aws" {
  alias                       = "no_region"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}

# iacp.placement.region_unverified (warn): region from a local.
# tflint-ignore: terraform_unused_declarations
provider "aws" {
  alias                       = "from_local"
  region                      = local.secondary_region
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}

# iacp.forbidden.module_source (rewritten to ?ref=main), plus the module's own
# encryption, public access, force_destroy and IAM violations.
module "store" {
  # Rewritten in plan.json to a ?ref=main git source by regenerate.sh.
  source = "../store"

  name          = "orders"
  secure        = false
  force_destroy = true
}

# iacp.s3.no_public_policy.
resource "aws_s3_bucket_policy" "public" {
  bucket = module.store.bucket
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::orders-data/*"
    }]
  })
}

# iacp.s3.no_public_acl.
resource "aws_s3_bucket_acl" "public" {
  bucket = module.store.bucket
  acl    = "public-read"
}

# iacp.encryption.s3_cmk: aws:kms with the AWS-managed key outside development.
resource "aws_s3_bucket" "logs" {
  bucket = "orders-logs"
}

resource "aws_s3_bucket_server_side_encryption_configuration" "logs" {
  bucket = aws_s3_bucket.logs.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "logs" {
  bucket                  = aws_s3_bucket.logs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# iacp.s3.policy_unknown (warn): computed Resource hides the document.
resource "aws_s3_bucket_policy" "logs" {
  bucket = aws_s3_bucket.logs.id
  policy = jsonencode({
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "arn:aws:iam::000000000000:root" }
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.logs.arn}/*"
    }]
  })
}

# iacp.iam.wildcard_action and iacp.iam.not_resource.
resource "aws_iam_policy" "admin_like" {
  name = "orders-admin-like"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Everything", Effect = "Allow", Action = "*", Resource = "arn:aws:s3:::orders-data" },
      { Sid = "AllButOne", Effect = "Allow", Action = "s3:GetObject", NotResource = "arn:aws:s3:::secret/*" },
    ]
  })
}

# iacp.iam.policy_unknown (warn): jsonencode() with a computed Resource.
resource "aws_iam_role_policy" "computed" {
  role = "orders-worker"
  policy = jsonencode({
    Statement = [{ Effect = "Allow", Action = "s3:PutObject", Resource = "${aws_s3_bucket.logs.arn}/*" }]
  })
}

# iacp.iam.admin_policy.
resource "aws_iam_role_policy_attachment" "admin" {
  role       = "orders-worker"
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

# iacp.iam.full_access_policy (warn).
resource "aws_iam_role_policy_attachment" "s3_full" {
  role       = "orders-worker"
  policy_arn = "arn:aws:iam::aws:policy/AmazonS3FullAccess"
}

# iacp.forbidden.data_source (count = 0 so nothing is fetched while planning).
# tflint-ignore: terraform_unused_declarations
data "http" "exfil" {
  count = 0
  url   = "https://example.invalid/collect"
}

# iacp.forbidden.provisioner.
resource "terraform_data" "bootstrap" {
  provisioner "local-exec" {
    command = "echo bootstrap"
  }
}

# iacp.encryption.sns_sse: no server-side encryption.
resource "aws_sns_topic" "plain" {
  name = "orders-plain"
}

# iacp.encryption.sns_kms: the AWS-managed key outside development.
resource "aws_sns_topic" "aws_managed" {
  name              = "orders-aws-managed"
  kms_master_key_id = "alias/aws/sns"
}

# iacp.encryption.dynamodb_cmk: the AWS owned key (no server_side_encryption
# block) outside development.
resource "aws_dynamodb_table" "plain" {
  name         = "orders-plain"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"

  attribute {
    name = "pk"
    type = "S"
  }
}

# iacp.encryption.rds_storage and iacp.network.rds_public.
resource "aws_db_instance" "public_plain" {
  identifier                  = "orders-public"
  engine                      = "postgres"
  instance_class              = "db.t4g.micro"
  allocated_storage           = 20
  username                    = "orders"
  manage_master_user_password = true
  storage_encrypted           = false
  publicly_accessible         = true
  skip_final_snapshot         = true
}

# iacp.encryption.rds_cmk: encrypted, but with the AWS-managed aws/rds key outside development.
resource "aws_db_instance" "default_key" {
  identifier                  = "orders-default-key"
  engine                      = "postgres"
  instance_class              = "db.t4g.micro"
  allocated_storage           = 20
  username                    = "orders"
  manage_master_user_password = true
  storage_encrypted           = true
  skip_final_snapshot         = true
}

# iacp.encryption.elasticache_at_rest and iacp.encryption.elasticache_transit.
resource "aws_elasticache_replication_group" "plain" {
  replication_group_id       = "orders-plain"
  description                = "unencrypted cache"
  engine                     = "redis"
  node_type                  = "cache.t4g.micro"
  num_cache_clusters         = 1
  at_rest_encryption_enabled = false
  transit_encryption_enabled = false
}

# iacp.encryption.elasticache_cmk: encrypted with the AWS-managed key outside development.
resource "aws_elasticache_replication_group" "default_key" {
  replication_group_id       = "orders-default-key"
  description                = "cache without a customer-managed key"
  engine                     = "redis"
  node_type                  = "cache.t4g.micro"
  num_cache_clusters         = 1
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
}

# iacp.ecr.scan_on_push, iacp.ecr.immutable_tags and iacp.encryption.ecr_kms.
resource "aws_ecr_repository" "loose" {
  name                 = "orders-loose"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = false
  }
}

# iacp.encryption.ecr_kms: KMS without kms_key is the AWS-managed aws/ecr key.
resource "aws_ecr_repository" "aws_managed_key" {
  name                 = "orders-aws-managed-key"
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "KMS"
  }
}

# iacp.network.ecs_public_ip.
resource "aws_ecs_service" "public" {
  name            = "orders-public"
  cluster         = "arn:aws:ecs:eu-central-1:000000000000:cluster/orders"
  task_definition = "orders:1"
  launch_type     = "FARGATE"
  desired_count   = 1

  network_configuration {
    subnets          = ["subnet-0aaaaaaaaaaaaaaa1"]
    assign_public_ip = true
  }
}
