#!/bin/bash

set -u
# usage: sh ./convert_sa_to_kube_config_go.sh tmp operator my-project
CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
CENTRAL_CLUSTER="e2e.operator.mongokubernetes.com"
NAMESPACE=$1
OPERATOR_NAMESPACE=$2
PROJECT_NAME=${3:-my-project}

# clean up MongoDBMulti resource from previous run
kubectl --context ${CENTRAL_CLUSTER} delete deployment mongodb-enterprise-operator --namespace ${OPERATOR_NAMESPACE} --ignore-not-found
kubectl --context ${CENTRAL_CLUSTER} delete mdbm --all --namespace ${OPERATOR_NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER1} delete sts --all --namespace ${NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER2} delete sts --all --namespace ${NAMESPACE} --ignore-not-found

go run tools/cmd/main.go -member-clusters ${CLUSTER1},${CLUSTER2} -central-cluster ${CENTRAL_CLUSTER} -member-cluster-namespace ${NAMESPACE} -central-cluster-namespace ${OPERATOR_NAMESPACE} -cleanup # -cluster-scoped

#kubectl --context ${CLUSTER1} label ns ${NAMESPACE} istio-injection=enabled
#kubectl --context ${CLUSTER2} label ns ${NAMESPACE} istio-injection=enabled

# deploy the MDB CRD in central cluster -- OM reconciler watches it
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodb.yaml
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_opsmanagers.yaml

## deploy the multi-cluster CRD
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodbmulti.yaml

# deploy the operator deployments
kubectl --context ${CENTRAL_CLUSTER} apply -f operator-deployment.yaml --namespace ${OPERATOR_NAMESPACE}

# deploy the CR
kubectl --context ${CENTRAL_CLUSTER} apply -f ./multi-cluster-CR.yaml --namespace ${OPERATOR_NAMESPACE}

# create OM admin secret
kubectl create secret generic ops-manager-admin-secret  --from-literal=Username="user.name@example.com" --from-literal=Password="Passw0rd."  --from-literal=FirstName="User" --from-literal=LastName="Name"

# deploy OM in central cluster
# kubectl --context ${CENTRAL_CLUSTER} apply -f ./om.yaml --namespace ${OPERATOR_NAMESPACE}

kubectl  --context ${CLUSTER1} --namespace "${NAMESPACE}" delete configmap my-project --ignore-not-found
kubectl  --context ${CLUSTER1} --namespace "${NAMESPACE}" delete configmap my-project --ignore-not-found
kubectl  --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" delete configmap my-project --ignore-not-found

kubectl  --context ${CLUSTER1} --namespace "${NAMESPACE}" delete secret my-credentials --ignore-not-found
kubectl  --context ${CLUSTER2} --namespace "${NAMESPACE}" delete secret my-credentials --ignore-not-found
kubectl  --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" delete secret my-credentials --ignore-not-found


# wait until the admin key secret has been created by the operator.
while : ; do
  kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} &>/dev/null && break
  echo "Waiting for admin secret to have been created..."
  sleep 15
done

# Configuring project
BASE_URL="$(kubectl --context ${CENTRAL_CLUSTER} get svc ops-manager-external-svc-ext -o wide -n ${OPERATOR_NAMESPACE} -o jsonpath='{.status.loadBalancer.ingress[*].hostname}')"
kubectl --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" create configmap my-project --from-literal=projectName="${PROJECT_NAME}" --from-literal=baseUrl="http://${BASE_URL}:8080"

# Configure the Kubernetes credentials for Ops Manager
API_KEY="$(kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} -o jsonpath='{.data.publicApiKey}' | base64 -d)"
USER="$(kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} -o jsonpath='{.data.user}' | base64 -d)"
kubectl --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" create secret generic my-credentials --from-literal=user="${USER}" --from-literal=publicApiKey="${API_KEY}"
