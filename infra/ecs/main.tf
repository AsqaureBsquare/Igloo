terraform {
  required_providers {
    aws       = { source = "hashicorp/aws",       version = "~> 5.0" }
    tailscale = { source = "tailscale/tailscale", version = "~> 0.17" }
  }
}

provider "aws" {
  region = var.region
}

provider "tailscale" {
  api_key = var.tailscale_api_key
  tailnet = var.tailscale_tailnet
}
