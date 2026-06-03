variable "workspace_id" {
  description = "Igloo workspace UUID"
  type        = string
}

variable "server_url" {
  description = "Igloo server URL reachable from inside the container"
  type        = string
}

variable "agent_token" {
  description = "JWT token the agent uses to authenticate with the server"
  type        = string
  sensitive   = true
}
