# ─── Karpenter IAM (via EKS module submodule) ────────────────────────────────

module "karpenter" {
  source  = "terraform-aws-modules/eks/aws//modules/karpenter"
  version = "~> 20.0"

  cluster_name = module.eks.cluster_name

  # IRSA for the Karpenter controller pod
  enable_irsa                     = true
  irsa_oidc_provider_arn          = module.eks.oidc_provider_arn
  irsa_namespace_service_accounts = ["kube-system:karpenter"]

  # Instance profile for nodes that Karpenter launches
  create_instance_profile = true

  # SQS queue for spot interruption + rebalance events
  enable_spot_termination = true

  tags = { Project = "igloo" }
}

# ─── Karpenter Helm release ───────────────────────────────────────────────────

resource "helm_release" "karpenter" {
  namespace        = "kube-system"
  name             = "karpenter"
  repository       = "oci://public.ecr.aws/karpenter"
  chart            = "karpenter"
  version          = "1.0.6"
  create_namespace = false
  wait             = true
  timeout          = 300

  repository_username = data.aws_ecrpublic_authorization_token.token.user_name
  repository_password = data.aws_ecrpublic_authorization_token.token.password

  set {
    name  = "settings.clusterName"
    value = module.eks.cluster_name
  }
  set {
    name  = "settings.interruptionQueue"
    value = module.karpenter.queue_name
  }
  set {
    name  = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
    value = module.karpenter.iam_role_arn
  }
  set {
    name  = "controller.resources.requests.cpu"
    value = "100m"
  }
  set {
    name  = "controller.resources.requests.memory"
    value = "128Mi"
  }
  set {
    name  = "controller.resources.limits.memory"
    value = "256Mi"
  }

  depends_on = [module.eks.eks_managed_node_groups]
}

# ─── Karpenter EC2NodeClass ───────────────────────────────────────────────────

resource "kubernetes_manifest" "karpenter_node_class" {
  manifest = {
    apiVersion = "karpenter.k8s.aws/v1"
    kind       = "EC2NodeClass"
    metadata = {
      name = "igloo-default"
    }
    spec = {
      amiSelectorTerms = [{ alias = "al2023@latest" }]
      role             = module.karpenter.node_iam_role_name
      subnetSelectorTerms = [
        for id in data.aws_subnets.public.ids : { id = id }
      ]
      securityGroupSelectorTerms = [{
        tags = { "karpenter.sh/discovery" = var.cluster_name }
      }]
      tags = { Project = "igloo" }
    }
  }

  depends_on = [helm_release.karpenter]
}

# ─── Karpenter NodePool (t3.micro) ────────────────────────────────────────────

resource "kubernetes_manifest" "karpenter_node_pool" {
  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "igloo-default"
    }
    spec = {
      template = {
        spec = {
          nodeClassRef = {
            group = "karpenter.k8s.aws"
            kind  = "EC2NodeClass"
            name  = "igloo-default"
          }
          requirements = [
            {
              key      = "kubernetes.io/arch"
              operator = "In"
              values   = ["amd64"]
            },
            {
              key      = "karpenter.sh/capacity-type"
              operator = "In"
              # spot first (cheaper); falls back to on-demand
              values = ["spot", "on-demand"]
            },
            {
              key      = "node.kubernetes.io/instance-type"
              operator = "In"
              # t3.micro free-tier eligible; add t3.small as fallback if spot unavailable
              values = ["t3.micro", "t3.small"]
            },
          ]
        }
      }
      # Scale to zero when idle — cost-saving
      disruption = {
        consolidationPolicy = "WhenEmpty"
        consolidateAfter    = "30s"
      }
      limits = {
        cpu    = "8"
        memory = "8Gi"
      }
    }
  }

  depends_on = [kubernetes_manifest.karpenter_node_class]
}
