# ── User-configurable variables (shown in UI at workspace creation) ────────────

variable "cpu" {
  description = "vCPU units (256=0.25 vCPU, 512=0.5 vCPU, 1024=1 vCPU, 2048=2 vCPU, 4096=4 vCPU)"
  type        = string
  default     = "512"
}

variable "memory" {
  description = "Memory in MB (512, 1024, 2048, 4096, 8192)"
  type        = string
  default     = "1024"
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

# ── Server-injected variables (set automatically, not shown in UI) ─────────────

variable "desired_count" {
  description = "ECS service desired task count (0=sleeping, 1=running)"
  type        = string
  default     = "1"
}

variable "workspace_id" {
  description = "Unique workspace ID"
  type        = string
}

variable "workspace_name" {
  description = "Human-readable workspace name"
  type        = string
}

variable "server_url" {
  description = "Igloo server URL — agent dials home here"
  type        = string
}

variable "agent_token" {
  description = "JWT the agent uses to authenticate with the server"
  type        = string
  sensitive   = true
}

variable "ssh_public_key" {
  description = "User SSH public key injected into authorized_keys"
  type        = string
  default     = ""
}

variable "cluster_name" {
  description = "ECS cluster name"
  type        = string
}

variable "task_execution_role_arn" {
  description = "ECS task execution role ARN"
  type        = string
}

variable "task_role_arn" {
  description = "ECS task role ARN"
  type        = string
}

variable "subnet_ids" {
  description = "Comma-separated private subnet IDs"
  type        = string
}

variable "security_group_id" {
  description = "Security group ID for the workspace task"
  type        = string
}

variable "workspace_image" {
  description = "ECR image URI for the workspace container"
  type        = string
}

variable "tailscale_auth_key" {
  description = "Tailscale one-time auth key — workspace joins the tailnet on boot"
  type        = string
  default     = ""
  sensitive   = true
}

variable "tailscale_hostname" {
  description = "Tailscale device hostname (e.g. igloo-my-workspace)"
  type        = string
  default     = ""
}
