#!/usr/bin/env bash

#
# Builds the `construction-site` namespace and docker config configmap
#

kubectl create namespace construction-site || true

kubectl -n construction-site create configmap docker-config --from-literal=config.json='{"credHelpers":{"268558157000.dkr.ecr.us-east-1.amazonaws.com":"ecr-login"}}'
