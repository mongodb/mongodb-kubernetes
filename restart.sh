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
kubectl delete deployment om-operator
kubectl delete clusterrole om-operator
kubectl delete serviceaccount om-operator
kubectl delete clusterrolebinding om-operator

echo "-- Compiling and building new container image"
CGO_ENABLED=0 GOOS=linux go build -o om-operator
eval $(minikube docker-env)
docker build -t om-operator:0.1 .

echo "-- Deploying new operator"
kubectl apply -f om-operator.yaml


# kubectl apply -f om-resource-sample.yaml
#
