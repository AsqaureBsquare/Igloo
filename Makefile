ECR              := 601804669600.dkr.ecr.us-east-1.amazonaws.com
REGION           := us-east-1
CLUSTER          := igloo
SERVICE          := igloo-server
PROV_SERVICE     := igloo-provisioner
EKS_CLUSTER      := igloo-eks
HELM_RELEASE     := igloo
HELM_CHART       := charts/igloo
HELM_VALUES      := charts/igloo/eks-values.yaml

.PHONY: build push deploy build-provisioner push-provisioner deploy-provisioner \
        build-workspace push-workspace infra ecr-login \
        eks-infra eks-kubeconfig helm-push helm-deploy helm-status

ecr-login:
	aws ecr get-login-password --region $(REGION) | \
		docker login --username AWS --password-stdin $(ECR)

# ─── Server ───────────────────────────────────────────────────────────────────

build: ecr-login
	docker build --platform linux/amd64 -t igloo-server .
	docker tag igloo-server:latest $(ECR)/igloo-server:latest

push: build
	docker push $(ECR)/igloo-server:latest

deploy: push
	$(eval TASK_DEF := $(shell aws ecs describe-task-definition --task-definition $(SERVICE) --region $(REGION) --query 'taskDefinition.taskDefinitionArn' --output text))
	aws ecs update-service --cluster $(CLUSTER) --service $(SERVICE) \
		--task-definition $(TASK_DEF) \
		--force-new-deployment --region $(REGION) --output text --query 'service.deployments[0].status'
	@echo "Waiting for service to stabilize..."
	@until aws ecs describe-services --cluster $(CLUSTER) --services $(SERVICE) \
		--region $(REGION) --query 'services[0].deployments | length(@)' \
		--output text | grep -q "^1$$"; do sleep 5; done
	@echo "Server deployed."

# ─── Provisioner ─────────────────────────────────────────────────────────────

build-provisioner: ecr-login
	GOOS=linux GOARCH=amd64 go build -o igloo-provisioner ./cmd/provisioner
	docker build --platform linux/amd64 -f Dockerfile.provisioner -t igloo-provisioner .
	docker tag igloo-provisioner:latest $(ECR)/igloo-provisioner:latest
	rm -f igloo-provisioner

push-provisioner: build-provisioner
	docker push $(ECR)/igloo-provisioner:latest

deploy-provisioner: push-provisioner
	$(eval TASK_DEF := $(shell aws ecs describe-task-definition --task-definition $(PROV_SERVICE) --region $(REGION) --query 'taskDefinition.taskDefinitionArn' --output text))
	aws ecs update-service --cluster $(CLUSTER) --service $(PROV_SERVICE) \
		--task-definition $(TASK_DEF) \
		--force-new-deployment --region $(REGION) --output text --query 'service.deployments[0].status'
	@echo "Waiting for provisioner to stabilize..."
	@until aws ecs describe-services --cluster $(CLUSTER) --services $(PROV_SERVICE) \
		--region $(REGION) --query 'services[0].deployments | length(@)' \
		--output text | grep -q "^1$$"; do sleep 5; done
	@echo "Provisioner deployed."

# ─── Workspace image ─────────────────────────────────────────────────────────

build-workspace: ecr-login
	GOOS=linux GOARCH=amd64 go build -o workspace/igloo-agent ./cmd/agent
	docker build --platform linux/amd64 -f workspace/Dockerfile -t igloo-workspace workspace/
	docker tag igloo-workspace:latest $(ECR)/igloo-workspace:latest
	rm -f workspace/igloo-agent

push-workspace: build-workspace
	docker push $(ECR)/igloo-workspace:latest

# ─── Infrastructure (ECS) ────────────────────────────────────────────────────

infra:
	cd infra/ecs && terraform apply -auto-approve

# ─── EKS infrastructure ───────────────────────────────────────────────────────

eks-infra:
	cd infra/eks && terraform init && terraform apply -auto-approve

eks-kubeconfig:
	aws eks update-kubeconfig --region $(REGION) --name $(EKS_CLUSTER)

# ─── Helm (EKS deploy) ────────────────────────────────────────────────────────
# Required env vars: IGLOO_JWT_SECRET, IGLOO_PROVISIONER_KEY, IGLOO_IRSA_ROLE_ARN
# Example:
#   export IGLOO_JWT_SECRET=$(openssl rand -hex 32)
#   export IGLOO_PROVISIONER_KEY=$(openssl rand -hex 32)
#   export IGLOO_IRSA_ROLE_ARN=$(cd infra/eks && terraform output -raw server_irsa_role_arn)
#   make helm-deploy

helm-push: push push-provisioner push-workspace

helm-deploy: helm-push eks-kubeconfig
	helm upgrade --install $(HELM_RELEASE) $(HELM_CHART) \
		-f $(HELM_VALUES) \
		--set server.jwtSecret="$(IGLOO_JWT_SECRET)" \
		--set provisioner.key="$(IGLOO_PROVISIONER_KEY)" \
		--set "server.serviceAccount.annotations.eks\.amazonaws\.com/role-arn=$(IGLOO_IRSA_ROLE_ARN)" \
		--wait --timeout 5m
	@echo ""
	@echo "Igloo deployed. Getting ALB URL..."
	@kubectl get ingress igloo-server -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "(ALB still provisioning — run: kubectl get ingress)"

helm-status:
	helm status $(HELM_RELEASE)
	kubectl get pods,svc,ingress -l app.kubernetes.io/instance=$(HELM_RELEASE)
