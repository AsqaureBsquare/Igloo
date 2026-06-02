variable "project" {
  description = "Project name prefix for all resources"
  type        = string
  default     = "igloo"
}

variable "region" {
  description = "AWS region"
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}
