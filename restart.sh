#!/usr/bin/env bash

# Use this to rebuild and reinstall the operator

echo "-- Removing objects managed by the operator"
kubectl delete mongodbreplicasets.mongodb.com --all
kubectl delete mongodbstandalones.mongodb.com --all
kubectl delete mongodbshardedclusters.mongodb.com --all

echo "-- Removing custom types"
kubectl delete crd mongodbreplicasets.mongodb.com
kubectl delete crd mongodbshardedclusters.mongodb.com
kubectl delete crd mongodbstandalones.mongodb.com

echo "-- Removing kubernetes objects and operator"
kubectl delete pv --all
kubectl delete statefulsets --all
kubectl delete configmaps --all
kubectl delete -f om-operator.yaml

echo "-- Compiling and building new container image"

if [ -z $BUILD_LOCALLY ]; then
    if ifconfig en0 | grep -e "inet\s" | awk '{ print $2}' | grep "192.168" > /dev/null; then
        # i'm at home, connect to local vm
        connect_to=build
    else
        # i'm at the office, i presume, connect to kuberator physical machine
        connect_to=kuberator
    fi
    echo "-- Building om-operator on Linux machine ($connect_to)"
    ssh $connect_to 'source ~/.profile ; cd go/src/github.com/10gen/ops-manager-kubernetes/ ; go build -o om-operator'
else
    echo "-- Cross compiling om-operator"
    CGO_ENABLED=0 GOOS=linux go build -o om-operator || exit 1
fi

eval $(minikube docker-env)
docker build -t om-operator:0.1 .

echo "-- Deploying new operator"
kubectl apply -f samples/my-config-map.yaml # TODO om-operator requires the 'ops-manager-config' config map for now
kubectl apply -f om-operator.yaml


# kubectl apply -f om-resource-sample.yaml
#
