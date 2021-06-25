#!/bin/bash

set -u

CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
CENTRAL_CLUSTER="e2e.operator.mongokubernetes.com"
NAMESPACE=$1
OPERATOR_NAMESPACE=$2


go run tools/cmd/main.go -member-clusters ${CLUSTER1},${CLUSTER2} -central-cluster ${CENTRAL_CLUSTER} -member-cluster-namespace ${NAMESPACE} -central-cluster-namespace ${OPERATOR_NAMESPACE} -cleanup # -cluster-scoped

# deploy the multi-cluster CRD
kubectl --context ${CENTRAL_CLUSTER} apply -f ../config/crd/bases/mongodb.com_mongodbmulti.yaml

# deploy the operator pod
kubectl --context ${CENTRAL_CLUSTER} apply -f operator-deployment.yaml --namespace ${OPERATOR_NAMESPACE}

# deploy the CR
kubectl --context ${CENTRAL_CLUSTER} apply -f ./multi-cluster-CR.yaml
