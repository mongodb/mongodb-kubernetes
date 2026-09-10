#!/usr/bin/env bash
set -euo pipefail

C0="${CLUSTER0_CONTEXT:-default/api-kubehosted-9h9y-p1-openshiftapps-com:6443/admin-kubehosted}"
C1="${CLUSTER1_CONTEXT:-/api-u5p3c2n7r3a9k9m-scnn-p1-openshiftapps-com:6443/kubehosted-admin}"
NS="${POC_NAMESPACE:-lsierant2}"
DELETE_NAMESPACES="${DELETE_NAMESPACES:-no}"

if [[ "${CONFIRM_CLEANUP:-no}" != "yes" ]]; then
  echo "Refusing cleanup. Set CONFIRM_CLEANUP=yes after reviewing this script." >&2
  exit 1
fi

delete_names() {
  local context="$1" kind="$2"
  shift 2
  kubectl --context="$context" -n "$NS" delete "$kind" "$@" \
    --ignore-not-found --wait=false >/dev/null
}

delete_names "$C0" mongodbuser.mongodb.com mc-no-mesh-user
delete_names "$C0" mongodbmulticluster.mongodb.com mdb

for _ in $(seq 1 60); do
  if ! kubectl --context="$C0" -n "$NS" get mongodbmulticluster.mongodb.com mdb >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

helm uninstall mc-no-mesh-lsierant2 --kube-context="$C0" -n "$NS" \
  --ignore-not-found >/dev/null

for context in "$C0" "$C1"; do
  kubectl --context="$context" -n "$NS" delete \
    service,statefulset,persistentvolumeclaim,configmap,secret \
    -l "mongodbmulticluster=$NS-mdb" --ignore-not-found --wait=false >/dev/null
done

delete_names "$C0" deployment.apps cluster1-api-proxy mc-no-mesh-relay
delete_names "$C1" deployment.apps mc-no-mesh-relay
delete_names "$C0" pod mc-no-mesh-client
delete_names "$C1" pod mc-no-mesh-client
delete_names "$C0" statefulset.apps mdb-0
delete_names "$C1" statefulset.apps mdb-1
delete_names "$C0" persistentvolumeclaim data-mdb-0-0
delete_names "$C1" persistentvolumeclaim data-mdb-1-0 data-mdb-1-1

delete_names "$C0" route.route.openshift.io mdb-0-0-external
delete_names "$C1" route.route.openshift.io mdb-1-0-external mdb-1-1-external

for context in "$C0" "$C1"; do
  delete_names "$context" service \
    mc-no-mesh-dns \
    mdb-0-0-svc-external \
    mdb-1-0-svc-external \
    mdb-1-1-svc-external \
    mdb-0-svc \
    mdb-1-svc \
    mdb-svc
  delete_names "$context" configmap \
    mc-no-mesh-ca \
    mc-no-mesh-relay \
    mdb-hostname-override
  delete_names "$context" secret \
    mc-no-mesh-mdb-agent-certs \
    mc-no-mesh-mdb-agent-certs-pem \
    mc-no-mesh-mdb-cert \
    mc-no-mesh-mdb-cert-pem \
    mc-no-mesh-user-password \
    mdb-mc-no-mesh-user-admin
  delete_names "$context" rolebinding.rbac.authorization.k8s.io \
    mc-no-mesh-operator \
    mongodb-kubernetes-appdb
  delete_names "$context" role.rbac.authorization.k8s.io \
    mc-no-mesh-operator \
    mongodb-kubernetes-appdb
  delete_names "$context" serviceaccount \
    mc-no-mesh-operator \
    mongodb-kubernetes-appdb \
    mongodb-kubernetes-database-pods \
    mongodb-kubernetes-ops-manager
done

delete_names "$C0" deployment.apps mc-no-mesh-operator
delete_names "$C0" service cluster1-api-proxy operator-webhook
delete_names "$C0" networkpolicy.networking.k8s.io cluster1-api-proxy
delete_names "$C0" configmap \
  cluster1-api-proxy \
  lsierant2-project \
  mc-no-mesh-operator-member-list \
  mongodb-kubernetes-operator-member-list
delete_names "$C0" secret \
  cluster1-api-proxy-credentials \
  image-registries-secret \
  mc-no-mesh-ca-key-pair \
  mc-no-mesh-operator-token \
  mongodb-enterprise-operator-multi-cluster-kubeconfig \
  my-credentials
delete_names "$C1" secret image-registries-secret mc-no-mesh-operator-token
delete_names "$C0" issuer.cert-manager.io mc-no-mesh-ca-issuer mc-no-mesh-selfsigned
delete_names "$C0" certificate.cert-manager.io mc-no-mesh-ca mc-no-mesh-mdb mc-no-mesh-mdb-agent
delete_names "$C0" imagestream.image.openshift.io mc-no-mesh-operator

if [[ "$DELETE_NAMESPACES" == "yes" ]]; then
  kubectl --context="$C0" delete namespace "$NS" --ignore-not-found --wait=false
  oc --context="$C1" delete project "$NS" --ignore-not-found --wait=false
elif [[ "$DELETE_NAMESPACES" != "no" ]]; then
  echo "DELETE_NAMESPACES must be yes or no" >&2
  exit 1
fi

echo "Named $NS PoC resources were submitted for deletion."
echo "The shared MongoDB CRDs, webhook, cert-manager, ClusterIssuers, source namespace, and existing lsierant PoC were not changed."
