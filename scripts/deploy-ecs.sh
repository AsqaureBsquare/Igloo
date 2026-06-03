#!/usr/bin/env bash
# Full deploy: build images → push to ECR → terraform apply → done.
set -euo pipefail

REGION="us-east-1"
APP="igloo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# ─── Step 1: Terraform init (ECR repos must exist before we can push) ─────────
echo "==> [1/5] Terraform init + ECR bootstrap..."
cd infra/ecs

# Generate JWT secret if not already in tfvars
if [ ! -f terraform.tfvars ]; then
  JWT=$(openssl rand -hex 32)
  cat > terraform.tfvars <<EOF
jwt_secret      = "$JWT"
server_image    = "placeholder"
workspace_image = "placeholder"
EOF
fi

terraform init -input=false
# Apply only ECR repos first so we can push images
terraform apply -auto-approve -input=false -target=aws_ecr_repository.server -target=aws_ecr_repository.workspace
cd "$ROOT"

# ─── Step 2: Get ECR URLs ─────────────────────────────────────────────────────
echo "==> [2/5] Fetching ECR repository URLs..."
ECR_SERVER=$(cd infra/ecs && terraform output -raw ecr_server)
ECR_WORKSPACE=$(cd infra/ecs && terraform output -raw ecr_workspace)
ACCOUNT_ID=$(echo "$ECR_SERVER" | cut -d. -f1)

echo "    Server image:    $ECR_SERVER"
echo "    Workspace image: $ECR_WORKSPACE"

# ─── Step 3: Build & push images ─────────────────────────────────────────────
echo "==> [3/5] Authenticating with ECR..."
aws ecr get-login-password --region "$REGION" | \
  docker login --username AWS --password-stdin "${ACCOUNT_ID}.dkr.ecr.${REGION}.amazonaws.com"

echo "==> Building igloo-agent (linux/amd64)..."
GOOS=linux GOARCH=amd64 go build -o workspace/igloo-agent ./cmd/agent

echo "==> Building + pushing workspace image..."
docker build \
  --platform linux/amd64 \
  -t "${ECR_WORKSPACE}:latest" \
  -f workspace/Dockerfile.local workspace/
docker push "${ECR_WORKSPACE}:latest"

echo "==> Building + pushing server image..."
docker build \
  --platform linux/amd64 \
  -t "${ECR_SERVER}:latest" \
  .
docker push "${ECR_SERVER}:latest"

# ─── Step 4: Full terraform apply ─────────────────────────────────────────────
echo "==> [4/5] Running terraform apply..."
cd infra/ecs

# Update tfvars with real image URIs
JWT=$(grep jwt_secret terraform.tfvars | cut -d'"' -f2)
cat > terraform.tfvars <<EOF
jwt_secret      = "$JWT"
server_image    = "${ECR_SERVER}:latest"
workspace_image = "${ECR_WORKSPACE}:latest"
EOF

terraform apply -auto-approve -input=false
URL=$(terraform output -raw url)
cd "$ROOT"

# ─── Step 5: Done ─────────────────────────────────────────────────────────────
echo ""
echo "==> [5/5] Done!"
echo ""
echo "  ✓ Igloo is live at: $URL"
echo ""
echo "  To view server logs:"
echo "    aws logs tail /ecs/igloo/server --follow --region $REGION"
echo ""
echo "  To tear everything down:"
echo "    cd infra/ecs && terraform destroy -auto-approve"
echo ""
