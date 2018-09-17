# General upgrade procedure to check upgrade works fine #

These are the steps we can follow on each release to make sure that upgrade works fine. Note, that OM instance and Kube cluster must be running and ready

> Some code can be removed (e.g. recreation of config map)

### Install and initialize environment for old version

```bash

kubectl delete ns mongodb

# go to the public repo
cd mongodb-enterprise-kubernetes

# checkout the release tag
git checkout 0.2

# install the operator  
helm delete --purge mongodb-enterprise; helm install helm_chart --namespace kube-system --name mongodb-enterprise

# create secret (get data from OM)
kubectl -n mongodb create secret generic my-credentials --from-literal=user=alisovenko@gmail.com --from-literal=publicApiKey=af4d3f6a-6e0f-446b-8288-8a8da09cf092

# create config map (old style  < 0.3 version)
kubectl create configmap my-project --from-literal projectId="5b9a2284a957713d7f6faa5a" --from-literal baseUrl=http://ec2-34-204-8-104.compute-1.amazonaws.com:8080

# create mongodb resources
kubectl apply -f samples/minimal/standalone.yaml
kubectl apply -f samples/minimal/replica-set.yaml
kubectl apply -f samples/minimal/sharded-cluster.yaml

# ... wait until OM goes green

```

### Perform the upgrade ###

> Note, that when performing upgrade 0.2 -> 0.3 and 0.3 -> 0.4 we cannot use "helm del" as it will remove the namespace.

```bash

# checkout the new release
git checkout 0.3

# delete old resources manually (aka helm delete --purge mongodb-enterprise). 
# this instruction is relevant only for 0.2! 0.3 will contain "role" instead of "clusterrole"

kubectl delete deployment mongodb-enterprise-operator
kubectl delete clusterrole mongodb-enterprise-operator
kubectl delete serviceaccount mongodb-enterprise-operator

# Update om connection config map (only for upgrading to 0.3)
kubectl delete configmap my-project
kubectl create configmap my-project --from-literal projectName="Project 0" --from-literal orgId="5b9a2284a957713d7f6faa57" --from-literal baseUrl=http://ec2-34-204-8-104.compute-1.amazonaws.com:8080 -n mongodb

# install new helm chart is impossible without removing old one (but we don't want to delete previous namespace)
# as it will give the error "Error: a release named mongodb-enterprise already exists."

# so we install operator from yaml files instead

kubectl apply -f crds.yaml
kubectl apply -f mongodb-enterprise.yaml

# verify that all mongodb resources were restarted in new containers


```
