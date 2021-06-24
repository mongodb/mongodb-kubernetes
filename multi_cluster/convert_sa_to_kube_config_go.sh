#!/bin/bash

CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
CENTRAL_CLUSTER="e2e.operator.mongokubernetes.com"
NAMESPACE=${1:-chatton}
OPERATOR_NAMESPACE="operator"
SA_NAME="mongodb-enterprise-operator-multi-cluster"


go run tools/cmd/main.go -member-clusters e2e.cluster1.mongokubernetes.com,e2e.cluster2.mongokubernetes.com -central-cluster ${CENTRAL_CLUSTER} -member-cluster-namespace ${NAMESPACE} -central-cluster-namespace ${OPERATOR_NAMESPACE} -cleanup

kubectl --context ${CENTRAL_CLUSTER} apply -f examples/service_account_using_go/central_cluster_resources --namespace ${OPERATOR_NAMESPACE}

