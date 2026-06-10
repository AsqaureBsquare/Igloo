terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.12"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
  }
}

provider "aws" {
  region = var.region
}

# Auth for helm/kubernetes providers — uses aws cli credential chain (same as terraform)
provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
    }
  }
}

# ─── EKS cluster ─────────────────────────────────────────────────────────────

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = "1.30"

  vpc_id     = data.aws_vpc.main.id
  subnet_ids = data.aws_subnets.public.ids

  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  # Explicit admin entry — same role handles both Terraform and CI kubectl
  enable_cluster_creator_admin_permissions = false

  access_entries = {
    github_actions = {
      principal_arn = "arn:aws:iam::601804669600:role/igloo-github-actions"
      policy_associations = {
        admin = {
          policy_arn   = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
  }

  # ── Bootstrap node group ──────────────────────────────────────────────────
  # Runs: kube-system, karpenter, aws-load-balancer-controller, coredns
  # t3.medium: 2 vCPU, 4 GiB — 17 max pods per node, enough for all system pods
  eks_managed_node_groups = {
    system = {
      name           = "${var.cluster_name}-system"
      instance_types = ["t3.small", "t3.medium"]
      capacity_type  = "SPOT"
      min_size       = 2
      max_size       = 4
      desired_size   = 2

      labels = {
        "igloo.dev/node-pool" = "system"
      }
    }
  }

  # Required tag for Karpenter node discovery
  node_security_group_tags = {
    "karpenter.sh/discovery" = var.cluster_name
  }

  # EKS add-ons (managed, auto-updated by AWS)
  cluster_addons = {
    coredns    = { most_recent = true }
    kube-proxy = { most_recent = true }
    vpc-cni    = { most_recent = true }
  }

  tags = { Project = "igloo" }
}
