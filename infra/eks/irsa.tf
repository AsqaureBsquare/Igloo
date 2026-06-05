# ─── IRSA: igloo-server → DynamoDB ───────────────────────────────────────────
# Pod assumes this role; no AWS access keys needed in the container.

data "aws_iam_policy_document" "server_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:sub"
      values   = ["system:serviceaccount:default:igloo-server"]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "igloo_server" {
  name               = "${var.cluster_name}-server-irsa"
  assume_role_policy = data.aws_iam_policy_document.server_assume.json
  tags               = { Project = "igloo" }
}

resource "aws_iam_role_policy" "igloo_server_dynamo" {
  name = "${var.cluster_name}-server-dynamo"
  role = aws_iam_role.igloo_server.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "dynamodb:GetItem",
          "dynamodb:PutItem",
          "dynamodb:DeleteItem",
          "dynamodb:Query",
          "dynamodb:Scan",
          "dynamodb:UpdateItem",
          "dynamodb:BatchGetItem",
          "dynamodb:BatchWriteItem",
          "dynamodb:DescribeTable",
        ]
        Resource = [
          # Core tables (created by infra/ecs)
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-users",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-users/index/*",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-workspaces",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-workspaces/index/*",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-templates",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-templates/index/*",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-ws-jobs",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-ws-jobs/index/*",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-provisioner-keys",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-provisioner-keys/index/*",
          # Single-table for orgs/teams (created by infra/ecs)
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-entities",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo-entities/index/*",
        ]
      },
      {
        # Allow server to create ECS tasks for workspace provisioning
        Effect = "Allow"
        Action = [
          "ecs:RunTask",
          "ecs:StopTask",
          "ecs:DescribeTasks",
          "ecs:DescribeTaskDefinition",
          "ecs:RegisterTaskDefinition",
          "iam:PassRole",
          "logs:CreateLogGroup",
          "logs:CreateLogStream",
          "logs:PutLogEvents",
          "logs:DescribeLogStreams",
          "ec2:DescribeSubnets",
          "ec2:DescribeSecurityGroups",
        ]
        Resource = ["*"]
      },
      {
        # S3 for Terraform state (workspace provisioner)
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
        Resource = ["arn:aws:s3:::igloo-*", "arn:aws:s3:::igloo-*/*"]
      }
    ]
  })
}
