provider "aws" {
  region = var.region
}

locals {
  subnet_ids = split(",", var.subnet_ids)
}

# ── Task definition ───────────────────────────────────────────────────────────
resource "aws_ecs_task_definition" "workspace" {
  family                   = "igloo-ws-${var.workspace_id}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = var.task_execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([{
    name      = "workspace"
    image     = var.workspace_image
    essential = true

    environment = [
      { name = "IGLOO_SERVER_URL",  value = var.server_url },
      { name = "IGLOO_AGENT_TOKEN", value = var.agent_token },
      { name = "IGLOO_SSH_KEY",     value = var.ssh_public_key },
    ]

    linuxParameters = { initProcessEnabled = true }

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = "/igloo/workspaces"
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = var.workspace_id
      }
    }
  }])

  tags = {
    WorkspaceId = var.workspace_id
    ManagedBy   = "igloo"
  }
}

# ── ECS Service ───────────────────────────────────────────────────────────────
# desired_count=0 → sleeping (zero cost), desired_count=1 → running
resource "aws_ecs_service" "workspace" {
  name             = "igloo-ws-${var.workspace_id}"
  cluster          = var.cluster_name
  task_definition  = aws_ecs_task_definition.workspace.arn
  desired_count    = tonumber(var.desired_count)
  launch_type      = "FARGATE"
  platform_version = "LATEST"

  network_configuration {
    subnets          = local.subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = true
  }

  tags = {
    WorkspaceId = var.workspace_id
    ManagedBy   = "igloo"
  }
}

# ── Outputs ───────────────────────────────────────────────────────────────────
output "service_name" {
  value = aws_ecs_service.workspace.name
}
