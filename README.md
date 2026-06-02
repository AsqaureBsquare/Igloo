# Igloo

Platform-agnostic serverless dev workspaces. Pay per active minute, not per month.

## How it works

- `igloo up` — spins up a dev workspace (container, VM, GPU, or bare metal)
- Idle for 10 mins — workspace auto-sleeps. Compute stops. Disk persists. Cost = $0.
- `igloo up` again — wakes in ~15 seconds. Same disk. Same state.

## Getting Started

### Prerequisites

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.6
- AWS CLI configured (`aws configure`)

### Step 1 — Bootstrap remote state

```bash
cd infra/bootstrap
terraform init
terraform apply
# Note the bucket name from the output
```

### Step 2 — Configure backend

Edit `infra/aws/backend.tf` and replace `REPLACE_WITH_BOOTSTRAP_OUTPUT_BUCKET` with the bucket name from step 1.

### Step 3 — Deploy infrastructure

```bash
cd infra/aws
terraform init
terraform apply
```

## Architecture

```
CLI (Go)
  └── API Gateway (REST + WebSocket)
        └── Lambda (control plane)
              ├── ECS Fargate (workspace containers)
              ├── EBS (persistent workspace disk)
              ├── DynamoDB (runtime state)
              └── SSM Session Manager (SSH — no open ports)
```

## License

Apache 2.0
