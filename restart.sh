#!/usr/bin/env bash

# Remove, rebuild and reinstall needed objects

kubectl delete mongodbreplicaset my-mongodb-replicaset
kubectl delete clusterrole om-operator
kubectl delete serviceaccount om-operator
kubectl delete clusterrolebinding om-operator
kubectl delete deployment om-operator

eval $(minikube docker-env)
docker build -t operator:0.1 .

kubectl apply -f om-operator.yaml
kubectl apply -f om-resource-sample.yaml
