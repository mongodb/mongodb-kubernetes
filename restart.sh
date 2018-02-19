#!/usr/bin/env bash

# Use this to rebuild and reinstall the operator

kubectl delete mongodbreplicaset my-mongodb-replicaset
kubectl delete clusterrole om-operator
kubectl delete serviceaccount om-operator
kubectl delete clusterrolebinding om-operator
kubectl delete deployment om-operator

kubectl delete crd mongodbreplicasets.mongodb.com
kubectl delete crd mongodbshardedcluster.mongodb.com
kubectl delete crd mongodbstandalones.mongodb.com

CGO_ENABLED=0 GOOS=linux go build -o om-operator
eval $(minikube docker-env)
docker build -t om-operator:0.1 .

# kubectl apply -f om-operator.yaml
# kubectl apply -f om-resource-sample.yaml
