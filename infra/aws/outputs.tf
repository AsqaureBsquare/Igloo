output "vpc_id" {
  value = module.networking.vpc_id
}

output "private_subnet_ids" {
  value = module.networking.private_subnet_ids
}

output "public_subnet_ids" {
  value = module.networking.public_subnet_ids
}

output "workspace_sg_id" {
  value = module.networking.workspace_sg_id
}

output "lambda_sg_id" {
  value = module.networking.lambda_sg_id
}

output "workspaces_table_name" {
  value = module.state.workspaces_table_name
}

output "workspace_data_bucket" {
  value = module.state.workspace_data_bucket
}

output "lambda_role_arn" {
  value = module.iam.lambda_role_arn
}

output "fargate_execution_role_arn" {
  value = module.iam.fargate_execution_role_arn
}

output "fargate_task_role_arn" {
  value = module.iam.fargate_task_role_arn
}

output "ecs_cluster_name" {
  value = module.workspace.ecs_cluster_name
}

output "ecr_repository_url" {
  value = module.workspace.ecr_repository_url
}

output "workspace_task_definition_arn" {
  value = module.workspace.workspace_task_definition_arn
}
