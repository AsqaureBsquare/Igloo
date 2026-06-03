data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# ─── ECS task execution role (pull images, write logs) ────────────────────────

resource "aws_iam_role" "execution" {
  name               = "${var.app}-execution-role"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# ─── Workspace task role (minimal — agent uses outbound WebSocket only) ────────

resource "aws_iam_role" "workspace" {
  name               = "${var.app}-workspace-role"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

# ─── Server task role ─────────────────────────────────────────────────────────
# Needs to run Terraform that creates/updates/destroys ECS services + task defs.

resource "aws_iam_role" "server" {
  name               = "${var.app}-server-role"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy" "server" {
  name = "${var.app}-server-policy"
  role = aws_iam_role.server.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # DynamoDB — users, workspaces, templates
      {
        Effect = "Allow"
        Action = [
          "dynamodb:GetItem",
          "dynamodb:PutItem",
          "dynamodb:DeleteItem",
          "dynamodb:Query",
          "dynamodb:Scan",
          "dynamodb:UpdateItem"
        ]
        Resource = [
          aws_dynamodb_table.users.arn,
          "${aws_dynamodb_table.users.arn}/index/*",
          aws_dynamodb_table.workspaces.arn,
          "${aws_dynamodb_table.workspaces.arn}/index/*",
          aws_dynamodb_table.templates.arn,
        ]
      },
      # ECS — Terraform creates/updates/destroys per-workspace services
      {
        Effect = "Allow"
        Action = [
          "ecs:RegisterTaskDefinition",
          "ecs:DeregisterTaskDefinition",
          "ecs:DescribeTaskDefinition",
          "ecs:CreateService",
          "ecs:UpdateService",
          "ecs:DeleteService",
          "ecs:DescribeServices",
          "ecs:ListServices",
          "ecs:DescribeClusters"
        ]
        Resource = "*"
      },
      # IAM PassRole — Terraform passes execution + workspace roles to ECS
      {
        Effect = "Allow"
        Action = ["iam:PassRole"]
        Resource = [
          aws_iam_role.execution.arn,
          aws_iam_role.workspace.arn,
        ]
      },
      # S3 — Terraform state for each workspace
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
        Resource = [
          aws_s3_bucket.tf_state.arn,
          "${aws_s3_bucket.tf_state.arn}/*",
        ]
      },
      # CloudWatch Logs — read workspace container logs
      {
        Effect   = "Allow"
        Action   = ["logs:FilterLogEvents", "logs:DescribeLogStreams", "logs:GetLogEvents"]
        Resource = "arn:aws:logs:*:*:log-group:/igloo/workspaces:*"
      }
    ]
  })
}
