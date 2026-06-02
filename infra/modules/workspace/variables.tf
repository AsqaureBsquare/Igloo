variable "project" {
  description = "Project name prefix"
  type        = string
  default     = "igloo"
}

variable "region" {
  description = "AWS region"
  type        = string
}

variable "fargate_execution_role_arn" {
  description = "ARN of the Fargate task execution IAM role"
  type        = string
}

variable "fargate_task_role_arn" {
  description = "ARN of the Fargate task IAM role"
  type        = string
}

variable "container_cpu" {
  description = "Default CPU units for container workspace (1024 = 1 vCPU)"
  type        = number
  default     = 512
}

variable "container_memory" {
  description = "Default memory (MB) for container workspace"
  type        = number
  default     = 1024
}

variable "workspace_image" {
  description = "Container image for workspace tasks. Defaults to public Ubuntu for dev/test. Set to ECR URL for production."
  type        = string
  default     = "public.ecr.aws/ubuntu/ubuntu:24.04_stable"
}
