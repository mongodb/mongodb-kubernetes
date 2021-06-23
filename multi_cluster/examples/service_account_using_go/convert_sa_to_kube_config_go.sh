#!/bin/bash

CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
CENTRAL_CLUSTER="e2e.operator.mongokubernetes.com"
NAMESPACE=${1:-chatton}
OPERATOR_NAMESPACE="operator"
SA_NAME="mongodb-enterprise-operator-multi-cluster"


# create operator namespace
kubectl --context ${CENTRAL_CLUSTER} delete ns ${OPERATOR_NAMESPACE} --ignore-not-found
kubectl --context ${CENTRAL_CLUSTER} create ns ${OPERATOR_NAMESPACE}

kubectl --context ${CENTRAL_CLUSTER} delete secret -n ${OPERATOR_NAMESPACE} -l "multi-cluster=true"
kubectl --context ${CENTRAL_CLUSTER} delete clusterrole -l "multi-cluster=true"
kubectl --context ${CENTRAL_CLUSTER} delete clusterrolebinding -l "multi-cluster=true"


# set up service account in cluster 1
kubectl --context ${CLUSTER1} delete ns ${NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER1} create ns ${NAMESPACE}

kubectl --context ${CLUSTER1} delete clusterrole -l "multi-cluster=true"
kubectl --context ${CLUSTER1} delete clusterrolebinding -l "multi-cluster=true"


# set up service account in cluster 2
kubectl --context ${CLUSTER2} delete ns ${NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER2} create ns ${NAMESPACE}

kubectl --context ${CLUSTER2} delete clusterrole -l "multi-cluster=true"
kubectl --context ${CLUSTER2} delete clusterrolebinding -l "multi-cluster=true"


go run tools/cmd/main.go -member-clusters e2e.cluster1.mongokubernetes.com,e2e.cluster2.mongokubernetes.com -central-cluster ${CENTRAL_CLUSTER} -member-cluster-namespace ${NAMESPACE} -central-cluster-namespace ${OPERATOR_NAMESPACE}

kubectl --context ${CENTRAL_CLUSTER} apply -f examples/service_account_using_go/central_cluster_resources --namespace ${OPERATOR_NAMESPACE}
