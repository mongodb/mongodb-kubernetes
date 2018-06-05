#!/usr/bin/env bash

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd "${DIR}" &> /dev/null || exit 1
cd "$(./gitroot)" || exit 1

kubectl delete -f samples/node-mongo-app.yaml; kubectl apply -f samples/node-mongo-app.yaml
sleep 2
kubectl logs -l app=test-mongo-app
