# Tailscale configuration — only applied when tailscale_api_key is set.
# Manages ACL tags and device policy for igloo workspace nodes.

locals {
  tailscale_enabled = var.tailscale_api_key != ""
}

# Tag ownership: igloo workspaces are tagged tag:igloo-workspace.
# This ACL allows workspace nodes to reach any destination (ssh, etc.)
# and allows igloo users to reach workspace nodes.
resource "tailscale_acl" "igloo" {
  count = local.tailscale_enabled ? 1 : 0

  acl = jsonencode({
    tagOwners = {
      "tag:igloo-workspace" = []
    }

    acls = [
      # Workspace nodes can reach anywhere (outbound internet, etc.)
      {
        action = "accept"
        src    = ["tag:igloo-workspace"]
        dst    = ["*:*"]
      },
      # All tailnet members can reach workspace nodes on any port
      {
        action = "accept"
        src    = ["autogroup:member"]
        dst    = ["tag:igloo-workspace:*"]
      },
    ]

    ssh = [
      # Allow SSH from any tailnet member to workspace nodes
      {
        action = "accept"
        src    = ["autogroup:member"]
        dst    = ["tag:igloo-workspace"]
        users  = ["autogroup:nonroot", "root"]
      },
    ]
  })
}
