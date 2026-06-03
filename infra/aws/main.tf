terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
  }

}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Project     = "igloo"
      ManagedBy   = "terraform"
      Environment = var.environment
    }
  }
}

resource "random_id" "suffix" {
  byte_length = 4
}

# ─── JWT signing key for WebSocket tickets ────────────────────────────────────
# Persisted in Terraform state so it survives re-applies without rotation.

resource "random_password" "ws_jwt_secret" {
  length  = 32
  special = false
}

resource "aws_ssm_parameter" "ws_jwt_secret" {
  name  = "/${var.project}/ws-signing-key"
  type  = "SecureString"
  value = random_password.ws_jwt_secret.result

  tags = { Name = "${var.project}-ws-signing-key" }
}

module "networking" {
  source   = "../modules/networking"
  project  = var.project
  region   = var.region
  vpc_cidr = var.vpc_cidr
}

module "state" {
  source  = "../modules/state"
  project = var.project
  suffix  = random_id.suffix.hex
}

module "iam" {
  source  = "../modules/iam"
  project = var.project
}

module "workspace" {
  source                     = "../modules/workspace"
  project                    = var.project
  region                     = var.region
  fargate_execution_role_arn = module.iam.fargate_execution_role_arn
  fargate_task_role_arn      = module.iam.fargate_task_role_arn
}

module "api" {
  source                      = "../modules/api"
  project                     = var.project
  lambda_role_arn             = module.iam.lambda_role_arn
  workspaces_table_name       = module.state.workspaces_table_name
ecs_cluster_name            = module.workspace.ecs_cluster_name
  ecs_cluster_arn             = module.workspace.ecs_cluster_arn
  task_definition_family      = module.workspace.workspace_task_definition_family
  workspace_sg_id             = module.networking.workspace_sg_id
  private_subnet_ids          = module.networking.private_subnet_ids
  fargate_task_role_arn       = module.iam.fargate_task_role_arn
  ecs_infrastructure_role_arn = module.iam.ecs_infrastructure_role_arn
}
