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
    openssh-server \
    less \
    && rm -rf /var/lib/apt/lists/*

# sshd config — keys and authorized_keys live on the EBS volume (/workspace/.ssh/)
RUN mkdir -p /run/sshd && \
    printf 'Port 22\nPermitRootLogin yes\nPubkeyAuthentication yes\nPasswordAuthentication no\nAuthorizedKeysFile /workspace/.ssh/authorized_keys\nHostKey /workspace/.ssh/ssh_host_rsa_key\nHostKey /workspace/.ssh/ssh_host_ed25519_key\nStrictModes no\n' \
    > /etc/ssh/igloo_sshd_config

# AWS CLI v2 (x86_64 — matches Fargate default)
RUN curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip \
    && unzip /tmp/awscliv2.zip -d /tmp \
    && /tmp/aws/install \
    && rm -rf /tmp/awscliv2.zip /tmp/aws

# code-server (VS Code in browser)
RUN curl -fsSL https://code-server.dev/install.sh | sh

# igloo-agent binary
COPY igloo-agent /usr/local/bin/igloo-agent
RUN chmod +x /usr/local/bin/igloo-agent

# Workspace mount point (EBS volume attached here)
RUN mkdir -p /workspace

WORKDIR /workspace

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
