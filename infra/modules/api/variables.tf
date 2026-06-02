variable "project" {
  type    = string
  default = "igloo"
}

variable "lambda_role_arn" {
  description = "IAM role ARN for Lambda execution"
  type        = string
}

variable "workspaces_table_name" {
  description = "DynamoDB table name for workspace state"
  type        = string
}

variable "ecs_cluster_name" {
  description = "ECS cluster name"
  type        = string
}

variable "ecs_cluster_arn" {
  description = "ECS cluster ARN (used for EventBridge event pattern filter)"
  type        = string
}

variable "task_definition_family" {
  description = "ECS task definition family name"
  type        = string
}

variable "workspace_sg_id" {
  description = "Security group ID for workspace containers"
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnet IDs for workspace placement"
  type        = list(string)
}

variable "fargate_task_role_arn" {
  description = "IAM role ARN for Fargate task runtime permissions"
  type        = string
}

variable "ecs_infrastructure_role_arn" {
  description = "IAM role ARN for ECS infrastructure (EBS volume lifecycle management)"
  type        = string
}

variable "idle_timeout_minutes" {
  description = "Minutes of inactivity before a workspace is auto-slept"
  type        = number
  default     = 10
}
