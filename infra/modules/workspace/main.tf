# ECS cluster + base workspace infrastructure

# ECS Cluster
resource "aws_ecs_cluster" "main" {
  name = "${var.project}-workspaces"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  tags = { Name = "${var.project}-workspaces" }
}

resource "aws_ecs_cluster_capacity_providers" "main" {
  cluster_name = aws_ecs_cluster.main.name

  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
    base              = 1
  }
}

# ECR repository — base workspace image
resource "aws_ecr_repository" "workspace" {
  name                 = "${var.project}/workspace"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = { Name = "${var.project}-workspace" }
}

resource "aws_ecr_lifecycle_policy" "workspace" {
  repository = aws_ecr_repository.workspace.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Keep last 10 images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 10
        }
        action = { type = "expire" }
      }
    ]
  })
}

# CloudWatch Log Group — all workspace container logs
resource "aws_cloudwatch_log_group" "workspaces" {
  name              = "/igloo/workspaces"
  retention_in_days = 30
}

# CloudWatch Log Group — Lambda control plane logs
resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/igloo/lambda"
  retention_in_days = 14
}

# Base Fargate task definition template (container workspace)
# Lambda will reference this and override CPU/memory per workspace type
resource "aws_ecs_task_definition" "workspace_container" {
  family                   = "${var.project}-workspace-container"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.container_cpu
  memory                   = var.container_memory
  execution_role_arn       = var.fargate_execution_role_arn
  task_role_arn            = var.fargate_task_role_arn

  container_definitions = jsonencode([
    {
      name      = "workspace"
      image     = var.workspace_image
      essential = true
      command   = ["sleep", "infinity"]

      # initProcessEnabled = true is required for ECS Exec (SSM agent injection)
      linuxParameters = {
        initProcessEnabled = true
      }

      environment = [
        { name = "IGLOO_PROJECT", value = var.project }
      ]

      mountPoints = [
        {
          containerPath = "/workspace"
          sourceVolume  = "workspace-data"
          readOnly      = false
        }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.workspaces.name
          "awslogs-region"        = var.region
          "awslogs-stream-prefix" = "workspace"
        }
      }

      portMappings = []
    }
  ])

  volume {
    name = "workspace-data"
    # EBS volume is attached dynamically per workspace by Lambda at task launch
    # configure_at_launch = true enables this
    configure_at_launch = true
  }

  tags = { Name = "${var.project}-workspace-container" }
}
