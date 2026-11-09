# A small throwaway EKS cluster for testing Igloo (see ../aws for the production layout).
# Two Graviton nodes in the default VPC's public subnets, so no NAT gateway to pay for. The VPC CNI runs
# with its network-policy agent on: without it a NetworkPolicy is accepted and silently not enforced,
# which `helm test` is there to catch. Tear down with `terraform destroy`.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  backend "s3" {
    bucket       = "igloo-tfstate-391924456536"
    key          = "aws-test/terraform.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}

variable "region" {
  default = "us-east-1"
}

variable "cluster_name" {
  default = "igloo-test"
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "igloo-test" }
  }
}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
  # EKS cannot place a control plane in us-east-1e.
  filter {
    name   = "availability-zone"
    values = ["us-east-1a", "us-east-1b", "us-east-1c", "us-east-1d", "us-east-1f"]
  }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = "1.31"

  vpc_id     = data.aws_vpc.default.id
  subnet_ids = data.aws_subnets.default.ids

  cluster_endpoint_public_access           = true
  enable_cluster_creator_admin_permissions = true

  cluster_addons = {
    coredns    = {}
    kube-proxy = {}
    vpc-cni = {
      before_compute       = true
      configuration_values = jsonencode({ enableNetworkPolicy = "true" })
    }
    aws-ebs-csi-driver = {}
  }

  eks_managed_node_groups = {
    main = {
      ami_type       = "AL2023_ARM_64_STANDARD"
      instance_types = ["t4g.large"]
      min_size       = 2
      max_size       = 2
      desired_size   = 2
      disk_size      = 40
      iam_role_additional_policies = {
        ebs = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
      }
    }
  }
}

output "cluster_name" {
  value = module.eks.cluster_name
}
