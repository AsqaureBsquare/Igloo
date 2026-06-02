# After running bootstrap, fill in the values from its outputs:
#   cd infra/bootstrap && terraform init && terraform apply
#   terraform output   <- copy bucket name and table name here
#
# Then run:
#   cd infra/aws && terraform init && terraform apply

terraform {
  backend "s3" {
    bucket         = "igloo-tf-state-62c78ed7"
    key            = "igloo/prod/terraform.tfstate"
    region         = "us-east-1"
    use_lockfile   = true
    encrypt        = true
  }
}
