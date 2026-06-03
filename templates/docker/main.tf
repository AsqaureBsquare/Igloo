terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}

provider "docker" {}

resource "docker_container" "workspace" {
  name  = "igloo-tf-${var.workspace_id}"
  image = "igloo-workspace:latest"
  rm    = false

  env = [
    "IGLOO_SERVER_URL=${var.server_url}",
    "IGLOO_AGENT_TOKEN=${var.agent_token}",
    "IGLOO_WORKSPACE_ID=${var.workspace_id}",
  ]

  host {
    host = "host.docker.internal"
    ip   = "host-gateway"
  }

  must_run = true
  restart  = "no"
}
