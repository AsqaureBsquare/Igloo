FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/root
ENV WORKSPACE=/workspace

# Common dev tools
RUN apt-get update && apt-get install -y \
    build-essential \
    curl \
    wget \
    git \
    vim \
    nano \
    htop \
    tmux \
    zsh \
    python3 \
    python3-pip \
    python3-venv \
    nodejs \
    npm \
    jq \
    unzip \
    ca-certificates \
    openssh-client \
    less \
    && rm -rf /var/lib/apt/lists/*

# AWS CLI v2 (x86_64 — matches Fargate default)
RUN curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip \
    && unzip /tmp/awscliv2.zip -d /tmp \
    && /tmp/aws/install \
    && rm -rf /tmp/awscliv2.zip /tmp/aws

# Workspace mount point (EBS volume attached here)
RUN mkdir -p /workspace

WORKDIR /workspace

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
