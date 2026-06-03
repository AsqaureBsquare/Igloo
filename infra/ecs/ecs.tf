resource "aws_ecs_cluster" "main" {
  name = var.app
}

resource "aws_cloudwatch_log_group" "server" {
  name              = "/ecs/${var.app}/server"
  retention_in_days = 7
}

# Log group for all workspace containers — must exist before first workspace starts.
resource "aws_cloudwatch_log_group" "workspaces" {
  name              = "/igloo/workspaces"
  retention_in_days = 3
}

# ─── Server task definition ───────────────────────────────────────────────────

resource "aws_ecs_task_definition" "server" {
  family                   = "${var.app}-server"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.server.arn

  container_definitions = jsonencode([{
    name  = "server"
    image = var.server_image

    portMappings = [{ containerPort = 8080, protocol = "tcp" }]

    environment = [
      { name = "AWS_REGION",                  value = var.region },
      { name = "IGLOO_JWT_SECRET",            value = var.jwt_secret },
      { name = "IGLOO_ECS_CLUSTER",           value = aws_ecs_cluster.main.arn },
      { name = "IGLOO_ECS_EXECUTION_ROLE_ARN", value = aws_iam_role.execution.arn },
      { name = "IGLOO_ECS_TASK_ROLE_ARN",     value = aws_iam_role.workspace.arn },
      { name = "IGLOO_ECS_SUBNETS",           value = join(",", aws_subnet.public[*].id) },
      { name = "IGLOO_ECS_SG",               value = aws_security_group.workspace.id },
      { name = "IGLOO_WORKSPACE_IMAGE",       value = var.workspace_image },
      { name = "IGLOO_TF_STATE_BUCKET",       value = aws_s3_bucket.tf_state.id },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.server.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "server"
      }
    }
  }])
}

# ─── Server ECS service ───────────────────────────────────────────────────────

resource "aws_ecs_service" "server" {
  name            = "${var.app}-server"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.server.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.server.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.server.arn
    container_name   = "server"
    container_port   = 8080
  }

  depends_on = [aws_lb_listener.http]

  lifecycle {
    ignore_changes = [task_definition]
  }
}
