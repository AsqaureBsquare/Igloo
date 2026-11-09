FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=vendor -ldflags="-s -w" -o igloo ./cmd/igloo
# Agent served to workspace pods at startup. The terraform provider's init script
# always requests igloo-agent-linux-amd64, so the file keeps that name even when
# AGENT_ARCH=arm64 (e.g. local Kubernetes on Apple Silicon).
ARG AGENT_ARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${AGENT_ARCH} go build -mod=vendor -ldflags="-s -w" -o igloo-agent-linux-amd64 ./cmd/agent

FROM alpine:3.19
ARG TERRAFORM_VERSION=1.9.8
ARG TARGETARCH
# SHA-256 of terraform_<version>_linux_<arch>.zip, copied from HashiCorp's SHA256SUMS
# after verifying its GPG signature (key C874 011F 0AB4 0511 0D02 1055 3436 5D94 72D7 468F).
# To upgrade: change the version and both digests; internal/provisioner/dockerfile_test.go
# keeps this file and Dockerfile.provisioner in step.
ARG TERRAFORM_SHA256_AMD64=186e0145f5e5f2eb97cbd785bc78f21bae4ef15119349f6ad4fa535b83b10df8
ARG TERRAFORM_SHA256_ARM64=f85868798834558239f6148834884008f2722548f84034c9b0f62934b2d73ebb
RUN apk add --no-cache ca-certificates tzdata unzip wget && \
    case "${TARGETARCH}" in \
      amd64) sum="${TERRAFORM_SHA256_AMD64}" ;; \
      arm64) sum="${TERRAFORM_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    wget -q https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip && \
    echo "${sum}  terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" | sha256sum -c - && \
    unzip terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip && \
    mv terraform /usr/local/bin/ && \
    rm terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip
WORKDIR /app
COPY --from=builder /app/igloo .
COPY --from=builder /app/igloo-agent-linux-amd64 downloads/igloo-agent-linux-amd64
COPY templates/ templates/
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://localhost:8080/health || exit 1
CMD ["./igloo", "server"]
