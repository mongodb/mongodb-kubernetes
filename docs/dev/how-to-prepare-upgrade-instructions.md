# General upgrade procedure BEFORE release to check upgrade works fine #

These are the steps we can follow before each release to make sure that upgrading an existing cluster works fine. Note, that an OM instance and Kube cluster must be running and ready.
Ideally this should be automated eventually


### Install and initialize environment for old version

```bash

kubectl delete ns mongodb
kubectl delete crds --all 

# go to the public repo
cd mongodb-enterprise-kubernetes

# checkout the previous release tag
git checkout release-0.5

# create namespace again
kubectl create ns mongodb

# install the operator  
# ... (for versions before 0.6)
helm delete --purge mongodb-enterprise; helm install public/helm_chart --namespace kube-system --name mongodb-enterprise
# ... (for versions after or equal 0.6 as our instructions have changed)
helm template public/helm_chart | kubectl apply -

# create secret and config map (get data from OM). Just replace exported variables values 
export ORG_ID="5c001d70c759e649e83fb87d"
export OM_BASE_URL="http://ec2-18-212-223-58.compute-1.amazonaws.com:8080"
export OM_API_KEY="27da2f10-3bb8-4059-b918-427c3b2de1eb"
export OM_USER="alisovenko@gmail.com"
kubectl -n mongodb create secret generic my-credentials --from-literal=user=${OM_USER} --from-literal=publicApiKey=${OM_API_KEY}
kubectl create configmap my-project --from-literal projectName="FooProject" --from-literal baseUrl=${OM_BASE_URL} -n mongodb
        
# create mongodb resources
kubectl apply -f public/samples/minimal/standalone.yaml
kubectl apply -f public/samples/minimal/replica-set.yaml
kubectl apply -f public/samples/minimal/sharded-cluster.yaml

# ... wait until OM goes green

```

### Perform the upgrade ###

Note, that this is a very basic upgrade procedure. If there's something special (config map/secret format has changed)
then these steps must be added and tested just for this specific release.


```bash

# checkout the new release
git checkout master

# generate yaml file - specify the tag of latest dev image built by Evergreen
helm template public/helm_chart \
  --set registry.repository="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev" \
  --set operator.version="b56d20a949830c3b2530a396594817a991a3ea9b" | kubectl apply -
  
# verify that all mongodb resources were restarted in new containers


```
