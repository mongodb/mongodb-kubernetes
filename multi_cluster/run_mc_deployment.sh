#!/bin/bash

set -u
# usage: sh ./run_mc_deployment.sh tmp operator my-project
CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
CENTRAL_CLUSTER="e2e.operator.mongokubernetes.com"
MDB_NAMESPACE=$1
OPERATOR_NAMESPACE=$2
PROJECT_NAME=${3:-my-project}

# clean up MongoDBMulti resource from previous run
kubectl --context ${CENTRAL_CLUSTER} delete deployment mongodb-enterprise-operator --namespace ${OPERATOR_NAMESPACE} --ignore-not-found
kubectl --context ${CENTRAL_CLUSTER} delete mdbm --all --namespace ${OPERATOR_NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER1} delete sts --all --namespace ${MDB_NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER2} delete sts --all --namespace ${MDB_NAMESPACE} --ignore-not-found

go run tools/cmd/main.go -member-clusters ${CLUSTER1},${CLUSTER2} -central-cluster ${CENTRAL_CLUSTER} -member-cluster-namespace ${MDB_NAMESPACE} -central-cluster-namespace ${OPERATOR_NAMESPACE} -cleanup

kubectl --context ${CLUSTER1} label ns ${MDB_NAMESPACE} istio-injection=enabled
kubectl --context ${CLUSTER2} label ns ${MDB_NAMESPACE} istio-injection=enabled

# deploy the CRDs in the central cluster

kubectl --context ${CLUSTER1} delete peerauthentication default --ignore-not-found
kubectl --context ${CLUSTER1}  -n ${MDB_NAMESPACE} apply -f - <<EOF
apiVersion: security.istio.io/v1beta1
kind: PeerAuthentication
metadata:
  name: "default"
spec:
  mtls:
    mode: STRICT
EOF

kubectl --context ${CLUSTER2} delete peerauthentication default --ignore-not-found
kubectl --context ${CLUSTER2}  -n ${MDB_NAMESPACE} apply -f - <<EOF
apiVersion: security.istio.io/v1beta1
kind: PeerAuthentication
metadata:
  name: "default"
spec:
  mtls:
    mode: STRICT
EOF


# deploy CRDs in central cluster
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodb.yaml
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_opsmanagers.yaml
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodbusers.yaml

## deploy the multi-cluster CRD
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodbmulti.yaml

sed -e "s/<NAMESPACE>/${OPERATOR_NAMESPACE}/g" < config/operator-deployment.yaml | kubectl --context "${CENTRAL_CLUSTER}" --namespace "${OPERATOR_NAMESPACE}" apply -f -

# deploy the database service account in member clusters
kubectl --context ${CLUSTER1} apply -f config/database-sa.yaml --namespace ${MDB_NAMESPACE}
kubectl --context ${CLUSTER2} apply -f config/database-sa.yaml --namespace ${MDB_NAMESPACE}

# deploy the CR
kubectl --context ${CENTRAL_CLUSTER} apply -f config/multi-cluster-CR.yaml --namespace ${MDB_NAMESPACE}

# create OM admin secret
kubectl create secret generic ops-manager-admin-secret  --from-literal=Username="user.name@example.com" --from-literal=Password="Passw0rd."  --from-literal=FirstName="User" --from-literal=LastName="Name"

# deploy OM in central cluster
# kubectl --context ${CENTRAL_CLUSTER} apply -f config/om.yaml --namespace ${OPERATOR_NAMESPACE}

kubectl  --context ${CLUSTER1} --namespace "${MDB_NAMESPACE}" delete configmap my-project --ignore-not-found
kubectl  --context ${CLUSTER2} --namespace "${MDB_NAMESPACE}" delete configmap my-project --ignore-not-found

kubectl  --context ${CLUSTER1} --namespace "${MDB_NAMESPACE}" delete secret my-credentials --ignore-not-found
kubectl  --context ${CLUSTER2} --namespace "${MDB_NAMESPACE}" delete secret my-credentials --ignore-not-found

kubectl  --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" delete configmap my-project --ignore-not-found
kubectl  --context ${CENTRAL_CLUSTER} --namespace "${OPERATOR_NAMESPACE}" delete secret my-credentials --ignore-not-found


# wait until the admin key secret has been created by the operator.
while : ; do
  kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} &>/dev/null && break
  echo "Waiting for admin secret to have been created..."
  sleep 15
done

# Configuring project
BASE_URL="$(kubectl --context ${CENTRAL_CLUSTER} get svc ops-manager-external-svc-ext -o wide -n ${OPERATOR_NAMESPACE} -o jsonpath='{.status.loadBalancer.ingress[*].hostname}')"
kubectl --context ${CENTRAL_CLUSTER} --namespace "${MDB_NAMESPACE}" create configmap my-project --from-literal=projectName="${PROJECT_NAME}" --from-literal=baseUrl="http://${BASE_URL}:8080"


# Configure the Kubernetes credentials for Ops Manager
API_KEY="$(kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} -o jsonpath='{.data.publicApiKey}' | base64 -d)"
USER="$(kubectl --context ${CENTRAL_CLUSTER} get secret "${OPERATOR_NAMESPACE}-ops-manager-external-admin-key" -n ${OPERATOR_NAMESPACE} -o jsonpath='{.data.user}' | base64 -d)"
kubectl --context ${CENTRAL_CLUSTER} --namespace "${MDB_NAMESPACE}" create secret generic my-credentials --from-literal=user="${USER}" --from-literal=publicApiKey="${API_KEY}"


# create user in central cluster
kubectl --context ${CENTRAL_CLUSTER} apply -f config/scram-user-secret.yaml --namespace ${OPERATOR_NAMESPACE}
