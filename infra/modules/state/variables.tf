variable "project" {
  description = "Project name prefix"
  type        = string
  default     = "igloo"
}

variable "suffix" {
  description = "Unique suffix for globally unique resource names (e.g. random hex)"
  type        = string
}
