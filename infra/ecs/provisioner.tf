resource "aws_ecr_repository" "provisioner" {
  name                 = "${var.app}-provisioner"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
}

resource "aws_cloudwatch_log_group" "provisioner" {
  name              = "/ecs/${var.app}/provisioner"
  retention_in_days = 7
}

resource "aws_ecs_task_definition" "provisioner" {
  family                   = "${var.app}-provisioner"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 1024
  memory                   = 2048
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.server.arn

  container_definitions = jsonencode([{
    name  = "provisioner"
    image = "${aws_ecr_repository.provisioner.repository_url}:latest"

    environment = [
      { name = "AWS_REGION",                   value = var.region },
      { name = "IGLOO_SERVER_URL",             value = coalesce(var.server_url, "http://${aws_lb.main.dns_name}") },
      { name = "IGLOO_ECS_CLUSTER",            value = aws_ecs_cluster.main.arn },
      { name = "IGLOO_ECS_EXECUTION_ROLE_ARN", value = aws_iam_role.execution.arn },
      { name = "IGLOO_ECS_TASK_ROLE_ARN",      value = aws_iam_role.workspace.arn },
      { name = "IGLOO_ECS_SUBNETS",            value = join(",", aws_subnet.public[*].id) },
      { name = "IGLOO_ECS_SG",                 value = aws_security_group.workspace.id },
      { name = "IGLOO_WORKSPACE_IMAGE",        value = var.workspace_image },
      { name = "IGLOO_TF_STATE_BUCKET",        value = aws_s3_bucket.tf_state.id },
      { name = "IGLOO_WORKER_CONCURRENCY",     value = "10" },
      { name = "IGLOO_PROVISIONER_KEY",         value = var.provisioner_key },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.provisioner.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "provisioner"
      }
    }
  }])
}

resource "aws_ecs_service" "provisioner" {
  name            = "${var.app}-provisioner"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.provisioner.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.server.id]
    assign_public_ip = true
  }

  lifecycle {
    ignore_changes = [task_definition]
  }
}
