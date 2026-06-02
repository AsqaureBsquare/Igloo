output "lambda_role_arn" {
  value = aws_iam_role.lambda.arn
}

output "lambda_role_name" {
  value = aws_iam_role.lambda.name
}

output "fargate_execution_role_arn" {
  value = aws_iam_role.fargate_execution.arn
}

output "fargate_task_role_arn" {
  value = aws_iam_role.fargate_task.arn
}

output "ecs_infrastructure_role_arn" {
  value = aws_iam_role.ecs_infrastructure.arn
}
