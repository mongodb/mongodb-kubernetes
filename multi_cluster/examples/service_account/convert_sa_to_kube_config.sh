#!/bin/bash

CLUSTER1="e2e.cluster1.mongokubernetes.com"
CLUSTER2="e2e.cluster2.mongokubernetes.com"
SERVER="https://api.${CLUSTER2}"
NAMESPACE=chatton
SA_NAME="can-read-pods"
#SA_NAME="cannot-read-pods"

kubectl --context ${CLUSTER2} delete ns ${NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER2} create ns ${NAMESPACE}

kubectl --context ${CLUSTER2} delete serviceaccount ${SA_NAME} --ignore-not-found
kubectl --context ${CLUSTER2} create serviceaccount ${SA_NAME} -n ${NAMESPACE} || true
SA2_SECRET_NAME="$(kubectl get --context ${CLUSTER2} secret -n ${NAMESPACE} | grep ${SA_NAME} | awk '{ print $ 1}')"

kubectl --context ${CLUSTER2} apply -f cluster2_resources -n ${NAMESPACE}

ca=$(kubectl get --context ${CLUSTER2} secret/${SA2_SECRET_NAME} -n ${NAMESPACE} -o jsonpath='{.data.ca\.crt}')
token=$(kubectl get --context ${CLUSTER2} secret/${SA2_SECRET_NAME} -n ${NAMESPACE} -o jsonpath='{.data.token}' | base64 --decode)
namespace=$(kubectl get --context ${CLUSTER2} secret/${SA2_SECRET_NAME}  -n ${NAMESPACE} -o jsonpath='{.data.namespace}' | base64 --decode)

echo "
apiVersion: v1
kind: Config
clusters:
- name: ${CLUSTER2}
  cluster:
    certificate-authority-data: ${ca}
    server: ${SERVER}
contexts:
- name: ${CLUSTER2}
  context:
    cluster: ${CLUSTER2}
    namespace: ${namespace}
    user: ${CLUSTER2}
current-context: ${CLUSTER2}
users:
- name: ${CLUSTER2}
  user:
    token: ${token}
" > kubeconfig


kubectl --context ${CLUSTER1} delete ns ${NAMESPACE} --ignore-not-found
kubectl --context ${CLUSTER1} create ns ${NAMESPACE}


kubectl delete --context ${CLUSTER1} configmap -n ${NAMESPACE} kubeconfig --ignore-not-found
kubectl create --context ${CLUSTER1} configmap -n ${NAMESPACE} kubeconfig --from-file=kubeconfig || true

kubectl apply --context ${CLUSTER1} -f cluster1_resources -n ${NAMESPACE}

rm kubeconfig

