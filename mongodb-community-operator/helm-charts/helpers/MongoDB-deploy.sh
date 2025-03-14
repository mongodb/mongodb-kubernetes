#!/bin/bash
set -euxo pipefail

# Helper script to automate OpsManager Key creating and setting correct ConfigMap and secret to deploy MongoDB
# Set following MongoDB Env variables:

# MongoDB name and namespace
MONGODB_NAMESPACE=mongodb
MONGODB_NAME=mongoreplset

# Information about Ops Manager deployment
OPS_MANAGER_NAMESPACE=opsmanager
OPS_MANAGER_HELM_RELEASE_NAME=opsmanager

# Name of Ops Manager org where MongoDB will be dployed
OPS_MANAGER_ORG_NAME=DemoOrg

## Few hints to automate OpsManager after installation and deploy first cluster

# Before moving on make sure OpsManager CR is running
until [ "$(kubectl get om -n $OPS_MANAGER_NAMESPACE -o=jsonpath='{.items[0].status.opsManager.phase}')" == Running ];
do
sleep 10s
done;

# Use mongocli to simplify setting up Ops Manager. https://docs.mongodb.com/mongocli
# This script assumes that access to OpsManager initial Global Admin key secret is not restricted

kubectl config set-context --current --namespace=$OPS_MANAGER_NAMESPACE

### Set up cli profile to connect to OpsManager that was provisioned by Operator
mongocli config set private_api_key "$(kubectl -n $OPS_MANAGER_NAMESPACE get secrets/$OPS_MANAGER_HELM_RELEASE_NAME-ops-manager-admin-key --template='{{.data.publicApiKey}}' | base64 -D)"

mongocli config set public_api_key "$(kubectl -n $OPS_MANAGER_NAMESPACE get secrets/$OPS_MANAGER_HELM_RELEASE_NAME-ops-manager-admin-key --template='{{.data.user}}' | base64 -D)"

# This works for EKS Load Balancer for other OM ingresses please use different aproach
mongocli config set ops_manager_url "http://$(kubectl -n $OPS_MANAGER_NAMESPACE get svc $OPS_MANAGER_HELM_RELEASE_NAME-ops-manager-svc-ext -o=jsonpath='{.status.loadBalancer.ingress[0].hostname}'):8080/"

mongocli config set service  "ops-manager"

OPS_MANAGER_ORG_ID=""
### Create new Organization `mongodbTest`
if [[ $(mongocli iam organizations ls --name $OPS_MANAGER_ORG_NAME -o go-template="{{ len .Results }}") -gt 0 ]]
then
OPS_MANAGER_ORG_ID=$(mongocli iam organizations ls --name $OPS_MANAGER_ORG_NAME  -o go-template="{{ (index .Results 0).ID }}" )
else
OPS_MANAGER_ORG_ID=$(mongocli iam organizations create $OPS_MANAGER_ORG_NAME -o go-template="{{ .ID }}" )
fi
### create API Key for the org we just created with Role ORG_OWNER


# Note: MongoDB CRD requires a secret
# kubectl create secret generic opsmanager-org-access-key  --from-literal="user=LFBQEYDP" --from-literal="publicApiKey=9af0ce8b-c88d-4521-a22f-db5fcacf8a9e"
# Note: Command to create Ops Manager API Key looks like:
# mongocli iam organizations apiKeys create --orgId $(mongocli iam organizations list --name mongodbTest -o go-template="{{ range .Results }} {{ .ID }} {{ end }}") --desc "My API key" --role ORG_OWNER -o go-template="{{ .PrivateKey }} {{.PublicKey}}"
kubectl create ns $MONGODB_NAMESPACE || true
kubectl config set-context --current --namespace=$MONGODB_NAMESPACE

# cleanup of the existing cm and secret
kubectl delete secret opsmanager-org-access-key || true
kubectl delete configmap opsmanager-configmap || true

# By putting two commands together we will get a somewhat complicated command that creates OpsManage API Key and generates Kubernetes secret `opsmanager-org-access-key`
mongocli iam organizations apiKeys create \
    --orgId "$(mongocli iam organizations describe "${OPS_MANAGER_ORG_ID}" -o go-template='{{ .ID }}')" \
    --desc "My API key" \
    --role ORG_OWNER \
    -o go-template='kubectl -n '"${MONGODB_NAMESPACE}"' create secret generic opsmanager-org-access-key  --from-literal="user={{.PublicKey}}"  --from-literal="publicApiKey={{ .PrivateKey }}" ' | bash
# create config map
kubectl create configmap opsmanager-configmap \
    --from-literal=projectName=$MONGODB_NAME \
    --from-literal=baseUrl="http://$(kubectl -n $OPS_MANAGER_NAMESPACE get svc $OPS_MANAGER_HELM_RELEASE_NAME-ops-manager-svc-ext -o=jsonpath='{.status.loadBalancer.ingress[0].hostname}'):8080" \
    --from-literal="orgId=$OPS_MANAGER_ORG_ID"
# We could use this secrete to deploy MongoDB CR.

# Deploy HELM Chart
pushd ../charts/ent-operator-database/
helm upgrade mongodb . --set opsManager.configMap=opsmanager-configmap --set opsManager.secretRef=opsmanager-org-access-key  -n "$MONGODB_NAMESPACE" --create-namespace -i
popd

until [ "$(kubectl get mdb -n $MONGODB_NAMESPACE -o=jsonpath='{.items[0].status.phase}')" = Running ];
do
sleep 10s
done;
