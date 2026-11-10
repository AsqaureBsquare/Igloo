#!/usr/bin/env bash
# Build the server and provisioner images for a LOCAL cluster and roll them out.
# Picks the workspace agent's architecture from the cluster's nodes: an agent built
# for the wrong architecture runs under emulation and crashes intermittently
# (Go's GC faults under x86 emulation on Apple Silicon).
#
#   scripts/dev-deploy.sh            # server + provisioner
#   scripts/dev-deploy.sh server     # one of them (server | provisioner | guard)
set -euo pipefail
cd "$(dirname "$0")/.."

node_arch=$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')   # arm64 | amd64
platform="linux/${node_arch}"
echo "cluster nodes: ${node_arch} -> building ${platform}, agent ${node_arch}"

what=${1:-all}
if [[ $what == all || $what == server ]]; then
  docker build --platform "$platform" --build-arg "AGENT_ARCH=${node_arch}" -t igloo-server:dev .
  kubectl rollout restart deploy/igloo-server
  kubectl rollout status deploy/igloo-server --timeout=180s
fi
if [[ $what == all || $what == guard ]]; then
  docker build --platform "$platform" -f Dockerfile.guard -t igloo-guard:dev .
  # The gateway is optional: only roll it out when the chart has installed it.
  if kubectl get deploy/igloo-guard >/dev/null 2>&1; then
    kubectl rollout restart deploy/igloo-guard
    kubectl rollout status deploy/igloo-guard --timeout=180s
  fi
fi
if [[ $what == all || $what == provisioner ]]; then
  # Restarting the provisioner stops it taking jobs and lets running ones finish.
  docker build --platform "$platform" -f Dockerfile.provisioner -t igloo-provisioner:dev .
  kubectl rollout restart deploy/igloo-provisioner
  kubectl rollout status deploy/igloo-provisioner --timeout=180s
fi
