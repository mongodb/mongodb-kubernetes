#!/usr/bin/env bash
set -xeuo pipefail

eval $(minikube docker-env)
docker build docker/automation-agent -t automation-agent -f docker/automation-agent/Dockerfile
