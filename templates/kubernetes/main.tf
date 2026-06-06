terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
  }
}

# Runs inside the EKS pod — automatically picks up in-cluster service account credentials.
provider "kubernetes" {}

variable "workspace_id"    {}
variable "workspace_name"  {}
variable "server_url"      {}
variable "agent_token"     {}
variable "workspace_image" {}
variable "desired_count"   { default = "1" }
variable "cpu"             { default = "500m" }
variable "memory"          { default = "512Mi" }
variable "disk"            { default = "10Gi" }

locals {
  name      = "ws-${var.workspace_id}"
  namespace = "workspaces"
  labels    = { "igloo.dev/workspace-id" = var.workspace_id }
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
          name  = "workspace"
          image = var.workspace_image
          env {
            name  = "IGLOO_SERVER_URL"
            value = var.server_url
          }
          env {
            name  = "IGLOO_AGENT_TOKEN"
            value = var.agent_token
          }
          env {
            name  = "IGLOO_WORKSPACE_ID"
            value = var.workspace_id
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
