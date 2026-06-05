# Reuse the VPC and public subnets created by infra/ecs
data "aws_vpc" "main" {
  tags = { Name = "igloo-vpc" }
}

data "aws_subnets" "public" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.main.id]
  }
  filter {
    name   = "tag:Name"
    values = ["igloo-public-0", "igloo-public-1"]
  }
}

# Existing ALB created by infra/ecs — EKS pods will be registered as a new target group on it
data "aws_lb" "main" {
  name = "igloo-alb"
}

data "aws_caller_identity" "current" {}

data "aws_ecrpublic_authorization_token" "token" {
  provider = aws.us_east_1
}

# Karpenter public ECR lives in us-east-1 — need a separate provider alias
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}
