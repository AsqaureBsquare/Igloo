terraform {
  required_providers {
    igloo = {
      source  = "AsqaureBsquare/igloo"
      version = "~> 0.1.4"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
  }
}

provider "igloo" {}
provider "kubernetes" {}

resource "igloo_agent" "main" {
  network_monitoring = false
}

resource "igloo_parameter" "workspace_image" {
  name         = "workspace_image"
  display_name = "Container Image"
  description  = "Any Docker image — the Igloo agent is injected automatically at startup."
  type         = "string"
  default      = "ubuntu:22.04"
  mutable      = false
}

resource "igloo_parameter" "cpu" {
  name         = "cpu"
  display_name = "CPU"
  description  = "CPU request for the workspace container."
  type         = "string"
  default      = "500m"
  mutable      = true

  option {
    name  = "0.5 CPU"
    value = "500m"
  }
  option {
    name  = "1 CPU"
    value = "1000m"
  }
  option {
    name  = "2 CPU"
    value = "2000m"
  }
}

resource "igloo_parameter" "memory" {
  name         = "memory"
  display_name = "Memory"
  description  = "Memory request for the workspace container."
  type         = "string"
  default      = "512Mi"
  mutable      = true

  option {
    name  = "512 Mi"
    value = "512Mi"
  }
  option {
    name  = "1 Gi"
    value = "1Gi"
  }
  option {
    name  = "2 Gi"
    value = "2Gi"
  }
}

resource "igloo_parameter" "disk" {
  name         = "disk"
  display_name = "Disk"
  description  = "Persistent volume size for the workspace home directory."
  type         = "string"
  default      = "10Gi"
  mutable      = false

  option {
    name  = "10 Gi"
    value = "10Gi"
  }
  option {
    name  = "20 Gi"
    value = "20Gi"
  }
  option {
    name  = "50 Gi"
    value = "50Gi"
  }
}

variable "desired_count" { default = "1" }

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
      requests = { storage = igloo_parameter.disk.value }
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
          name    = "workspace"
          image   = igloo_parameter.workspace_image.value
          command = ["sh", "-c", igloo_agent.main.init_script]
          resources {
            requests = {
              cpu    = igloo_parameter.cpu.value
              memory = igloo_parameter.memory.value
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
