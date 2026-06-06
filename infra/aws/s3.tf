resource "aws_s3_bucket" "ws_state" {
  bucket        = "igloo-ws-state-${data.aws_caller_identity.current.account_id}"
  force_destroy = true
  tags          = { Project = "igloo" }
}

resource "aws_s3_bucket_versioning" "ws_state" {
  bucket = aws_s3_bucket.ws_state.id
  versioning_configuration {
    status = "Enabled"
  }
}
