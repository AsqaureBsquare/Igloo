output "function_arns" {
  description = "Map of function name → ARN"
  value       = { for k, v in aws_lambda_function.functions : k => v.arn }
}

output "function_names" {
  description = "Map of function name → Lambda function name"
  value       = { for k, v in aws_lambda_function.functions : k => v.function_name }
}

output "layer_arn" {
  description = "ARN of the shared core Lambda layer"
  value       = aws_lambda_layer_version.core.arn
}
