resource "aws_ecr_repository" "server" {
  name                 = "${var.app}-server"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
}

resource "aws_ecr_repository" "workspace" {
  name                 = "${var.app}-workspace"
  image_tag_mutability = "MUTABLE"
  force_delete         = true
}
