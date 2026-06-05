output "cluster_name" {
  value = module.eks.cluster_name
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

output "configure_kubectl" {
  description = "Run this to configure kubectl"
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}

output "server_irsa_role_arn" {
  description = "Annotate igloo-server ServiceAccount with this ARN"
  value       = aws_iam_role.igloo_server.arn
}

output "alb_controller_role_arn" {
  value = aws_iam_role.alb_controller.arn
}

output "karpenter_node_role_name" {
  value = module.karpenter.node_iam_role_name
}
