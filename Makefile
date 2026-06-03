ECR              := 601804669600.dkr.ecr.us-east-1.amazonaws.com
REGION           := us-east-1
CLUSTER          := igloo
SERVICE          := igloo-server
PROV_SERVICE     := igloo-provisioner

.PHONY: build push deploy build-provisioner push-provisioner deploy-provisioner infra ecr-login

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

# ─── Infrastructure ───────────────────────────────────────────────────────────

infra:
	cd infra/ecs && terraform apply -auto-approve
