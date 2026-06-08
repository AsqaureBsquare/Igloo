terraform {
  required_providers {
    igloo = {
      source  = "AsqaureBsquare/igloo"
      version = "~> 0.1"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
  }
}

provider "igloo" {}
provider "kubernetes" {}

resource "igloo_agent" "main" {}

variable "workspace_image" {}
variable "desired_count"   { default = "1" }
variable "cpu"             { default = "500m" }
variable "memory"          { default = "512Mi" }
variable "disk"            { default = "10Gi" }

locals {
  name      = "ws-${igloo_agent.main.workspace_id}"
  namespace = "workspaces"
  labels    = { "igloo.dev/workspace-id" = igloo_agent.main.workspace_id }
}

resource "kubernetes_persistent_volume_claim" "workspace" {
  metadata {
    name      = local.name
    namespace = local.namespace
    labels    = local.labels
  }
  spec {
    access_modes       = ["ReadWriteOnce"]
    storage_class_name = "gp2"
    resources {
      requests = { storage = var.disk }
    }
  }
  wait_until_bound = false
  lifecycle {
    ignore_changes = [metadata, spec]
  }
}

resource "kubernetes_deployment" "workspace" {
  wait_for_rollout = false
  metadata {
    name      = local.name
    namespace = local.namespace
    labels    = local.labels
  }
  spec {
    replicas = tonumber(var.desired_count)
    selector {
      match_labels = local.labels
    }
    template {
      metadata {
        labels = local.labels
      }
      spec {
        container {
          name              = "workspace"
          image             = var.workspace_image
          image_pull_policy = "Always"
          env {
            name  = "IGLOO_SERVER_URL"
            value = igloo_agent.main.server_url
          }
          env {
            name  = "IGLOO_AGENT_TOKEN"
            value = igloo_agent.main.token
          }
          env {
            name  = "IGLOO_WORKSPACE_ID"
            value = igloo_agent.main.workspace_id
          }
          resources {
            requests = {
              cpu    = var.cpu
              memory = var.memory
            }
          }
          volume_mount {
            name       = "data"
            mount_path = "/home/user"
          }
        }
        volume {
          name = "data"
          persistent_volume_claim {
            claim_name = local.name
          }
        }
      }
    }
  }
  lifecycle {
    ignore_changes = [spec[0].template[0].spec[0].container[0].image]
  }
}
