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

# ─── IRSA: igloo-provisioner → S3 (Terraform state) ──────────────────────────

data "aws_iam_policy_document" "provisioner_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:sub"
      values   = ["system:serviceaccount:default:igloo-provisioner"]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "igloo_provisioner" {
  name               = "${var.cluster_name}-provisioner-irsa"
  assume_role_policy = data.aws_iam_policy_document.provisioner_assume.json
  tags               = { Project = "igloo" }
}

resource "aws_iam_role_policy" "igloo_provisioner_s3" {
  name = "${var.cluster_name}-provisioner-s3"
  role = aws_iam_role.igloo_provisioner.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
        Resource = ["arn:aws:s3:::igloo-*", "arn:aws:s3:::igloo-*/*"]
      }
    ]
  })
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
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo",
          "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/igloo/index/*",
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

# ─── IRSA: EBS CSI driver ─────────────────────────────────────────────────────

data "aws_iam_policy_document" "ebs_csi_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:sub"
      values   = ["system:serviceaccount:kube-system:ebs-csi-controller-sa"]
    }
    condition {
      test     = "StringEquals"
      variable = "${module.eks.oidc_provider}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ebs_csi" {
  name               = "${var.cluster_name}-ebs-csi-irsa"
  assume_role_policy = data.aws_iam_policy_document.ebs_csi_assume.json
  tags               = { Project = "igloo" }
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  role       = aws_iam_role.ebs_csi.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}
