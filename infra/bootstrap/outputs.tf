output "tf_state_bucket" {
  description = "S3 bucket name for Terraform remote state — paste into infra/aws/backend.tf"
  value       = aws_s3_bucket.tf_state.bucket
}

output "tf_lock_table" {
  description = "DynamoDB table name for state locking — paste into infra/aws/backend.tf"
  value       = aws_dynamodb_table.tf_lock.name
}
