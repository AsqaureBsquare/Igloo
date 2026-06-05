#!/bin/bash
# Deploy Igloo + Dex on minikube for local OIDC testing.
# Usage: ./dev/deploy-minikube-oidc.sh
set -e

MINIKUBE_IP=$(minikube ip)
echo "Minikube IP: $MINIKUBE_IP"

# ── 1. Build server image into minikube's Docker daemon ─────────────────────
eval $(minikube docker-env)
docker build -t igloo-server:dev .

# ── 2. Deploy Dex ────────────────────────────────────────────────────────────
helm repo add dex https://charts.dexidp.io 2>/dev/null || true
helm repo update dex

# Patch Minikube IP into the Dex config
DEX_VALUES=$(mktemp)
sed "s/MINIKUBE_IP/$MINIKUBE_IP/g" dev/dex-values.yaml > "$DEX_VALUES"

helm upgrade --install dex dex/dex \
  --namespace dex --create-namespace \
  --values "$DEX_VALUES" \
  --wait

rm "$DEX_VALUES"
echo "Dex ready at http://$MINIKUBE_IP:32000/dex"

# ── 3. Deploy Igloo ──────────────────────────────────────────────────────────
JWT_SECRET=$(openssl rand -hex 32)
PROV_KEY=$(openssl rand -hex 32)

helm upgrade --install igloo ./charts/igloo \
  --set server.image.repository=igloo-server \
  --set server.image.tag=dev \
  --set server.image.pullPolicy=Never \
  --set provisioner.image.repository=igloo-server \
  --set provisioner.image.tag=dev \
  --set provisioner.image.pullPolicy=Never \
  --set server.jwtSecret="$JWT_SECRET" \
  --set provisioner.key="$PROV_KEY" \
  --set server.localMode=true \
  --set server.serverURL="http://$MINIKUBE_IP:30080" \
  --set server.service.type=NodePort \
  --set server.service.nodePort=30080 \
  --set server.oidc.enabled=true \
  --set server.oidc.issuerURL="http://$MINIKUBE_IP:32000/dex" \
  --set server.oidc.clientID=igloo \
  --set server.oidc.clientSecret=igloo-dev-secret \
  --set "server.oidc.redirectURL=http://$MINIKUBE_IP:30080/api/auth/oidc/callback" \
  --set server.oidc.buttonText="Sign in with Dex" \
  --wait

echo ""
echo "Done!"
echo "  Igloo UI:  http://$MINIKUBE_IP:30080"
echo "  Dex UI:    http://$MINIKUBE_IP:32000/dex"
echo ""
echo "Test users (password: 'password'):"
echo "  admin@example.com   (becomes admin on first login)"
echo "  dev@example.com"
