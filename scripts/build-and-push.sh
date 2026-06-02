#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REGION="us-east-1"

echo "==> Getting ECR repository URL from Terraform..."
cd "$REPO_ROOT/infra/aws"
ECR_URL=$(terraform output -raw ecr_repository_url)
cd "$REPO_ROOT"

echo "==> ECR: $ECR_URL"

echo "==> Authenticating with ECR..."
aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "$ECR_URL"

echo "==> Building workspace image (linux/amd64)..."
docker build \
  --platform linux/amd64 \
  --tag igloo-workspace:latest \
  "$REPO_ROOT/workspace"

echo "==> Tagging and pushing..."
docker tag igloo-workspace:latest "$ECR_URL:latest"
docker push "$ECR_URL:latest"

echo ""
echo "Done. Image available at: $ECR_URL:latest"
