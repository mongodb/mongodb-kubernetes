#!/usr/bin/env sh

kube_ca_file="/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

if ! kubectl -n default get configmap/ca-certificates; then
    echo "Creating the ca-certificates for this Kubernetes cluster"
    kubectl -n default create configmap ca-certificates --from-file=ca-pem=${kube_ca_file}
else
    echo "ca-certificates configmap already exists."
fi
