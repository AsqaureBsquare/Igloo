terraform {
  required_providers {
    igloo = {
      source  = "AsqaureBsquare/igloo"
      version = "~> 0.1.6"
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

# Egress gateway. The Igloo server fills these in for a workspace created with a guard
# policy (users cannot set them); empty means the workspace is not guarded.
variable "guard_proxy" {
  default   = ""
  sensitive = true # carries the workspace's gateway credential
}
variable "guard_token" {
  default   = ""
  sensitive = true
}
variable "guard_route_base" { default = "" }
variable "guard_required" { default = "" }
variable "guard_namespace" { default = "default" } # where the Igloo server and gateway run
variable "guard_server_service" { default = "igloo-server" } # the Igloo server's Service name
variable "workspace_namespace" { default = "workspaces" }

locals {
  name      = "ws-${igloo_agent.main.workspace_id}"
  namespace = var.workspace_namespace
  labels    = { "igloo.dev/workspace-id" = igloo_agent.main.workspace_id }
  # Only the yes/no is revealed: the proxy URL itself carries a credential and stays
  # sensitive, so it never shows up in plans or in the build log.
  guarded = nonsensitive(var.guard_proxy != "")

  # The gateway's own address (for credential routes) and the Igloo server bypass the proxy:
  # the workspace talks to both directly, and the network policy below allows exactly those
  # two. Every other host, including other services inside the cluster, goes through the
  # gateway and its policy.
  gateway_host = var.guard_route_base != "" ? regex("^https?://([^/:]+)", var.guard_route_base)[0] : ""
  no_proxy = join(",", compact([
    "localhost", "127.0.0.1", local.gateway_host,
    var.guard_server_service, "${var.guard_server_service}.${var.guard_namespace}",
    "${var.guard_server_service}.${var.guard_namespace}.svc", "${var.guard_server_service}.${var.guard_namespace}.svc.cluster.local",
  ]))
  # igloo-guard.<namespace>.svc.cluster.local -> igloo-guard
  gateway_service = local.gateway_host != "" ? split(".", local.gateway_host)[0] : "igloo-guard"
  proxy_env = {
    HTTP_PROXY        = var.guard_proxy
    HTTPS_PROXY       = var.guard_proxy
    http_proxy        = var.guard_proxy
    https_proxy       = var.guard_proxy
    NO_PROXY          = local.no_proxy
    no_proxy          = local.no_proxy
    IGLOO_ROUTE_BASE  = var.guard_route_base
    IGLOO_ROUTE_TOKEN = var.guard_token
  }
  proxy_env_names = ["HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "IGLOO_ROUTE_BASE", "IGLOO_ROUTE_TOKEN"]
}

# A guarded workspace has no DNS. A name lookup is a way to carry data out (the name itself is the
# message, and whoever runs the name server reads it), and the gateway resolves the names a
# workspace asks it for, so the workspace only needs the addresses of the two things it talks to
# directly: the Igloo server and the gateway. They are written into the pod's /etc/hosts.
data "kubernetes_service" "igloo_server" {
  count = local.guarded ? 1 : 0
  metadata {
    name      = var.guard_server_service
    namespace = var.guard_namespace
  }
}

data "kubernetes_service" "igloo_gateway" {
  count = local.guarded ? 1 : 0
  metadata {
    name      = local.gateway_service
    namespace = var.guard_namespace
  }
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
        # A workspace has no use for the Kubernetes API, so it gets no service-account token to steal.
        automount_service_account_token = false
        # Guarded: no DNS (see the data sources above). Lookups fail at once instead of waiting.
        dns_policy = local.guarded ? "None" : "ClusterFirst"
        dynamic "dns_config" {
          for_each = local.guarded ? [1] : []
          content {
            nameservers = ["127.0.0.1"]
          }
        }
        dynamic "host_aliases" {
          for_each = local.guarded ? {
            (data.kubernetes_service.igloo_server[0].spec[0].cluster_ip) = [
              var.guard_server_service, "${var.guard_server_service}.${var.guard_namespace}",
              "${var.guard_server_service}.${var.guard_namespace}.svc", "${var.guard_server_service}.${var.guard_namespace}.svc.cluster.local",
            ]
            (data.kubernetes_service.igloo_gateway[0].spec[0].cluster_ip) = [
              local.gateway_service, "${local.gateway_service}.${var.guard_namespace}",
              "${local.gateway_service}.${var.guard_namespace}.svc", "${local.gateway_service}.${var.guard_namespace}.svc.cluster.local",
            ]
          } : {}
          content {
            ip        = host_aliases.key
            hostnames = host_aliases.value
          }
        }
        container {
          name    = "workspace"
          image   = igloo_parameter.workspace_image.value
          command = ["sh", "-c", igloo_agent.main.init_script]
          dynamic "env" {
            for_each = local.guarded ? toset(local.proxy_env_names) : toset([])
            content {
              name  = env.key
              value = local.proxy_env[env.key]
            }
          }
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
    # A workspace created with a guard policy must never run without its gateway.
    precondition {
      condition     = var.guard_required != "true" || var.guard_proxy != ""
      error_message = "This workspace requires the egress gateway, but the Igloo server did not provide one (is guard.enabled set in the chart?)."
    }
  }
}

# A guarded workspace can reach only DNS, the Igloo server, and the gateway. Everything
# else, including a connection that ignores the proxy settings, is dropped by the cluster's
# network policy, so the gateway cannot be bypassed from inside the workspace.
resource "kubernetes_network_policy_v1" "guard" {
  count = local.guarded ? 1 : 0
  metadata {
    name      = "${local.name}-guard"
    namespace = local.namespace
    labels    = local.labels
  }
  spec {
    pod_selector {
      match_labels = local.labels
    }
    # No DNS egress either: the pod's /etc/hosts holds the two addresses it needs.
    # Nothing may connect into the workspace either: its agent dials out, and everything
    # (terminal, VS Code, files) travels over that connection.
    policy_types = ["Egress", "Ingress"]

    egress { # the Igloo server (the workspace's agent dials home)
      to {
        namespace_selector {
          match_labels = { "kubernetes.io/metadata.name" = var.guard_namespace }
        }
        pod_selector {
          match_labels = { "app.kubernetes.io/name" = var.guard_server_service }
        }
      }
      ports {
        port     = "8080"
        protocol = "TCP"
      }
    }
    egress { # the egress gateway
      to {
        namespace_selector {
          match_labels = { "kubernetes.io/metadata.name" = var.guard_namespace }
        }
        pod_selector {
          match_labels = { "app.kubernetes.io/name" = local.gateway_service }
        }
      }
      ports {
        port     = "3128"
        protocol = "TCP"
      }
    }
  }
}
