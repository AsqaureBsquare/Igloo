output "workspace_data_bucket" {
  value = aws_s3_bucket.workspace_data.bucket
}

output "workspaces_table_name" {
  value = aws_dynamodb_table.workspaces.name
}

output "workspaces_table_arn" {
  value = aws_dynamodb_table.workspaces.arn
}
