terraform {
  backend "s3" {
    bucket = "igloo-infra-state"
    key    = "aws/terraform.tfstate"
    region = "us-east-1"
  }
}
