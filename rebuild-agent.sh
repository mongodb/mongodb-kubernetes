#!/usr/bin/env bash
set -eo pipefail

eval $(minikube docker-env)
docker build docker/automation-agent -t ops-manager-agent -f docker/automation-agent/Dockerfile
