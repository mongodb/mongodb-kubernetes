#!/usr/bin/env bash
set -xeuo pipefail

eval $(minikube docker-env)
docker build docker/database -t mongodb-enterprise-database -f docker/database/Dockerfile
