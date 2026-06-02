output "ecs_cluster_name" {
  value = aws_ecs_cluster.main.name
}

output "ecs_cluster_arn" {
  value = aws_ecs_cluster.main.arn
}

output "ecr_repository_url" {
  value = aws_ecr_repository.workspace.repository_url
}

output "workspace_task_definition_arn" {
  value = aws_ecs_task_definition.workspace_container.arn
}

output "workspace_task_definition_family" {
  value = aws_ecs_task_definition.workspace_container.family
}

output "workspaces_log_group" {
  value = aws_cloudwatch_log_group.workspaces.name
}
