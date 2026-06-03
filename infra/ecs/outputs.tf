output "url" {
  description = "Igloo server URL — open this in your browser"
  value       = "http://${aws_lb.main.dns_name}"
}

output "ecr_server" {
  value = aws_ecr_repository.server.repository_url
}

output "ecr_workspace" {
  value = aws_ecr_repository.workspace.repository_url
}

output "ecs_cluster" {
  value = aws_ecs_cluster.main.name
}
