data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  region     = data.aws_region.current.name
}

# ─── Lambda execution role ────────────────────────────────────────────────────

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${var.project}-lambda-role"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "lambda_basic" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy_attachment" "lambda_vpc" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

# Custom policy — Lambda control plane permissions
data "aws_iam_policy_document" "lambda_control_plane" {
  # ECS — manage workspace tasks
  statement {
    sid    = "ECSManage"
    effect = "Allow"
    actions = [
      "ecs:RunTask",
      "ecs:StopTask",
      "ecs:DescribeTasks",
      "ecs:ListTasks",
      "ecs:RegisterTaskDefinition",
      "ecs:DeregisterTaskDefinition",
      "ecs:DescribeTaskDefinition",
      "ecs:TagResource",
      "ecs:ExecuteCommand",
    ]
    resources = ["*"]
  }

  # EC2 — EBS volume management
  statement {
    sid    = "EBSManage"
    effect = "Allow"
    actions = [
      "ec2:CreateVolume",
      "ec2:DeleteVolume",
      "ec2:AttachVolume",
      "ec2:DetachVolume",
      "ec2:DescribeVolumes",
      "ec2:DescribeVolumeStatus",
      "ec2:ModifyVolume",
      "ec2:CreateSnapshot",
      "ec2:DeleteSnapshot",
      "ec2:DescribeSnapshots",
      "ec2:CreateTags",
    ]
    resources = ["*"]
  }

  # DynamoDB — workspace state
  statement {
    sid    = "DynamoDBWorkspaces"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
      "dynamodb:Scan",
    ]
    resources = [
      "arn:aws:dynamodb:${local.region}:${local.account_id}:table/${var.project}-workspaces",
      "arn:aws:dynamodb:${local.region}:${local.account_id}:table/${var.project}-workspaces/index/*",
    ]
  }

  # SSM — start sessions for workspace connectivity
  statement {
    sid    = "SSMSessions"
    effect = "Allow"
    actions = [
      "ssm:StartSession",
      "ssm:TerminateSession",
      "ssm:DescribeSessions",
      "ssm:GetConnectionStatus",
      "ssm:DescribeInstanceInformation",
    ]
    resources = ["*"]
  }

  # IAM PassRole — pass Fargate task role when running tasks
  statement {
    sid     = "PassRole"
    effect  = "Allow"
    actions = ["iam:PassRole"]
    resources = [
      aws_iam_role.fargate_execution.arn,
      aws_iam_role.fargate_task.arn,
      aws_iam_role.ecs_infrastructure.arn,
    ]
  }

  # S3 — workspace data bucket
  statement {
    sid    = "S3WorkspaceData"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:ListBucket",
    ]
    resources = [
      "arn:aws:s3:::${var.project}-workspace-data-*",
      "arn:aws:s3:::${var.project}-workspace-data-*/*",
    ]
  }

  # CloudWatch Logs
  statement {
    sid    = "CloudWatchLogs"
    effect = "Allow"
    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
      "logs:DescribeLogStreams",
    ]
    resources = ["arn:aws:logs:${local.region}:${local.account_id}:log-group:/igloo/*"]
  }

  # WebSocket API — PostToConnection for the relay Lambdas
  statement {
    sid    = "WebSocketRelay"
    effect = "Allow"
    actions = ["execute-api:ManageConnections"]
    resources = ["arn:aws:execute-api:${local.region}:${local.account_id}:*/*/@connections/*"]
  }

  # DynamoDB — WebSocket connection tracking
  statement {
    sid    = "DynamoDBWSConnections"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
    ]
    resources = [
      "arn:aws:dynamodb:${local.region}:${local.account_id}:table/${var.project}-ws-connections",
      "arn:aws:dynamodb:${local.region}:${local.account_id}:table/${var.project}-ws-connections/index/*",
    ]
  }

  # SSM — read JWT signing key and WebSocket endpoint URL at runtime
  statement {
    sid    = "SSMReadSecrets"
    effect = "Allow"
    actions = ["ssm:GetParameter"]
    resources = [
      "arn:aws:ssm:${local.region}:${local.account_id}:parameter/${var.project}/ws-signing-key",
      "arn:aws:ssm:${local.region}:${local.account_id}:parameter/${var.project}/ws-api-endpoint",
    ]
  }
}

resource "aws_iam_role_policy" "lambda_control_plane" {
  name   = "${var.project}-lambda-control-plane"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_control_plane.json
}

# ─── Fargate task execution role ──────────────────────────────────────────────

data "aws_iam_policy_document" "fargate_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fargate_execution" {
  name               = "${var.project}-fargate-execution-role"
  assume_role_policy = data.aws_iam_policy_document.fargate_assume.json
}

resource "aws_iam_role_policy_attachment" "fargate_execution" {
  role       = aws_iam_role.fargate_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Allow pulling from ECR
data "aws_iam_policy_document" "fargate_execution_extra" {
  statement {
    sid    = "ECRPull"
    effect = "Allow"
    actions = [
      "ecr:GetAuthorizationToken",
      "ecr:BatchCheckLayerAvailability",
      "ecr:GetDownloadUrlForLayer",
      "ecr:BatchGetImage",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "CloudWatchLogs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["arn:aws:logs:${local.region}:${local.account_id}:log-group:/igloo/*"]
  }
}

resource "aws_iam_role_policy" "fargate_execution_extra" {
  name   = "${var.project}-fargate-execution-extra"
  role   = aws_iam_role.fargate_execution.id
  policy = data.aws_iam_policy_document.fargate_execution_extra.json
}

# ─── Fargate task role (runtime permissions for workspace container) ───────────

resource "aws_iam_role" "fargate_task" {
  name               = "${var.project}-fargate-task-role"
  assume_role_policy = data.aws_iam_policy_document.fargate_assume.json
}

data "aws_iam_policy_document" "fargate_task" {
  # SSM Session Manager agent — required for in-container SSM connectivity
  statement {
    sid    = "SSMAgent"
    effect = "Allow"
    actions = [
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "CloudWatchLogs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["arn:aws:logs:${local.region}:${local.account_id}:log-group:/igloo/*"]
  }
}

resource "aws_iam_role_policy" "fargate_task" {
  name   = "${var.project}-fargate-task"
  role   = aws_iam_role.fargate_task.id
  policy = data.aws_iam_policy_document.fargate_task.json
}

# ─── ECS Infrastructure Role — required for managedEBSVolume attachment ───────
# This is separate from the task role. ECS uses it to manage EBS volume lifecycle.
# Must trust ecs.amazonaws.com (not ecs-tasks.amazonaws.com).

data "aws_iam_policy_document" "ecs_infra_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_infrastructure" {
  name               = "${var.project}-ecs-infrastructure-role"
  assume_role_policy = data.aws_iam_policy_document.ecs_infra_assume.json
}

resource "aws_iam_role_policy_attachment" "ecs_infrastructure_volumes" {
  role       = aws_iam_role.ecs_infrastructure.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSInfrastructureRolePolicyForVolumes"
}
