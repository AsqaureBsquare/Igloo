# Runtime state storage for Igloo workspaces

# S3 bucket — workspace artifacts, user data exports, logs
resource "aws_s3_bucket" "workspace_data" {
  bucket        = "${var.project}-workspace-data-${var.suffix}"
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "workspace_data" {
  bucket = aws_s3_bucket.workspace_data.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "workspace_data" {
  bucket = aws_s3_bucket.workspace_data.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "workspace_data" {
  bucket                  = aws_s3_bucket.workspace_data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# DynamoDB table — runtime workspace state
# PK: workspace_id
# GSI: user_id for listing workspaces per user
resource "aws_dynamodb_table" "workspaces" {
  name         = "${var.project}-workspaces"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "workspace_id"

  attribute {
    name = "workspace_id"
    type = "S"
  }

  attribute {
    name = "user_id"
    type = "S"
  }

  attribute {
    name = "status"
    type = "S"
  }

  # List all workspaces for a user
  global_secondary_index {
    name            = "user_id-index"
    hash_key        = "user_id"
    range_key       = "status"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  tags = { Name = "${var.project}-workspaces" }
}
