terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
    igloo = {
      source  = "AsqaureBsquare/igloo"
      version = "~> 0.1.6" # keep equal to the templates; prebake_test.go enforces it
    }
  }
  backend "local" {}
}

provider "aws" {
  region = "us-east-1"
}
