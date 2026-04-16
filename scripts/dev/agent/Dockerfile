# Build agent from mms-automation source for MCK testing
# Uses production agent image as base, replaces only the agent binary
#
# Usage: docker build --secret id=github_token,env=GITHUB_TOKEN -f Dockerfile ...

ARG BASE_AGENT_IMAGE=268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent:108.0.12.8846-1

FROM golang:1.25 AS builder

ENV CGO_ENABLED=1
ENV GOPRIVATE=github.com/10gen/*
ENV GOTOOLCHAIN=local

RUN apt-get update && apt-get install -y gcc libc-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY go_planner/ ./go_planner/

WORKDIR /app/go_planner/src/com.tengen/cm
RUN --mount=type=secret,id=github_token \
    if [ -f /run/secrets/github_token ]; then \
        git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download && rm -f ~/.gitconfig

RUN --mount=type=secret,id=github_token \
    if [ -f /run/secrets/github_token ]; then \
        git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go build -mod=mod -o /app/mongodb-agent ./main && rm -f ~/.gitconfig

# Use production agent image as base (has all scripts, tools, dependencies)
FROM ${BASE_AGENT_IMAGE}

USER root

# Replace only the agent binary with our custom build
COPY --from=builder /app/mongodb-agent /agent/mongodb-agent
RUN chmod +x /agent/mongodb-agent

USER 2000
