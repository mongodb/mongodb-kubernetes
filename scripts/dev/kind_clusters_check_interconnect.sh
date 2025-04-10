#!/usr/bin/env bash

set -euo pipefail
source scripts/dev/set_env_context.sh

function usage() {
  echo "This scripts has been designed to work in conjunction with recreate_kind_clusters.sh and verifies if inter-cluster connectivity works fine.

Usage:
  kind_clusters_check_interconnect.sh [-h] [-r]

Options:
  -h                   (optional) Shows this screen.
  -r                   (optional) Recreates namespaces before testing. Useful for iterative testing with cleanup.
  -u                   (optional) Prevents undeploying services. Useful for iterative testing.
"
  exit 0
}

install_echo() {
  ctx=$1
  cluster_no=$2
  ns=$3
  recreate=$4

  if [[ "${recreate}" == "true" ]]; then
    kubectl --context "${ctx}" delete namespace "${ns}" --wait
  fi

  kubectl --context "${ctx}" create namespace "${ns}" || true
  kubectl --context "${ctx}" label namespace "${ns}" istio-injection=enabled || true

  kubectl apply --context "${ctx}" -n "${ns}" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echoserver${cluster_no}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echoserver${cluster_no}
  template:
    metadata:
      labels:
        app: echoserver${cluster_no}
    spec:
      containers:
        - image: k8s.gcr.io/echoserver:1.10
          imagePullPolicy: Always
          name: echoserver${cluster_no}
          ports:
            - containerPort: 8080
EOF

  kubectl apply --context "${ctx}" -n "${ns}" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: echoserver${cluster_no}
spec:
  ports:
    - port: 8080
      targetPort: 8080
      protocol: TCP
  selector:
    app: echoserver${cluster_no}
EOF

  kubectl apply --context "${ctx}" -n "${ns}" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: echoserver${cluster_no}-lb
spec:
  ports:
    - port: 8080
      targetPort: 8080
      protocol: TCP
  selector:
    app: echoserver${cluster_no}
  type: LoadBalancer
EOF
}

test_connectivity() {
  first_context=$1
  first_idx=$2
  second_context=$3
  second_idx=$4

  pod1=$(kubectl get pods --context "${first_context}" -n "${NAMESPACE}" -o name | grep "echoserver${first_idx}")
  pod2=$(kubectl get pods --context "${second_context}" -n "${NAMESPACE}" -o name | grep "echoserver${second_idx}")

  lbpod1=$(kubectl get svc --context "${first_context}" -n "${NAMESPACE}" echoserver"${first_idx}"-lb -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')
  lbpod2=$(kubectl get svc --context "${second_context}" -n "${NAMESPACE}" echoserver"${second_idx}"-lb -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')

  echo "Checking own service connection"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl http://echoserver${first_idx}.${NAMESPACE}.svc.cluster.local:8080"
  echo "Checking own LB service connection"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl http://${lbpod1}:8080"
  echo "Checking service connection from ${first_context} to ${second_context}"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl http://echoserver${second_idx}.${NAMESPACE}.svc.cluster.local:8080"
  echo "Checking LB service connection from ${first_context} to ${second_context}"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl http://${lbpod2}:8080"
  echo "Checking service connection from ${second_context} to ${first_context}"
  kubectl exec --context "${second_context}" -n "${NAMESPACE}" "${pod2}" -- /bin/bash -c "curl http://echoserver${first_idx}.${NAMESPACE}.svc.cluster.local:8080"
  echo "Checking LB service connection from ${second_context} to ${first_context}"
  kubectl exec --context "${second_context}" -n "${NAMESPACE}" "${pod2}" -- /bin/bash -c "curl http://${lbpod1}:8080"
}

wait_for_deployment() {
  ctx=$1
  idx=$2

  echo "Waiting for echoserver${idx} deployment"
  kubectl wait --context "${ctx}" -n "${NAMESPACE}" --for=condition=available --timeout=120s "deployment/echoserver${idx}"
}

undeploy() {
  ctx=$1
  idx=$2

  echo "Deleting echoserver${idx}"
  kubectl delete deployment --context "${ctx}" -n "${NAMESPACE}" "echoserver${idx}"
}

recreate="false"
undeploy="true"
while getopts ':hru' opt; do
  # shellcheck disable=SC2220
  case ${opt} in
    h) usage ;;
    r) recreate="true" ;;
    u) undeploy="false" ;;
  esac
done
shift "$((OPTIND - 1))"

install_echo "kind-e2e-cluster-1" 1 "${NAMESPACE}" "${recreate}" &
install_echo "kind-e2e-cluster-2" 2 "${NAMESPACE}" "${recreate}" &
install_echo "kind-e2e-cluster-3" 3 "${NAMESPACE}" "${recreate}" &

wait

wait_for_deployment "kind-e2e-cluster-1" 1
wait_for_deployment "kind-e2e-cluster-2" 2
wait_for_deployment "kind-e2e-cluster-3" 3

wait

test_connectivity "kind-e2e-cluster-1" 1 "kind-e2e-cluster-2" 2
test_connectivity "kind-e2e-cluster-2" 2 "kind-e2e-cluster-1" 1

test_connectivity "kind-e2e-cluster-2" 2 "kind-e2e-cluster-3" 3
test_connectivity "kind-e2e-cluster-3" 3 "kind-e2e-cluster-2" 2

test_connectivity "kind-e2e-cluster-3" 3 "kind-e2e-cluster-1" 1
test_connectivity "kind-e2e-cluster-1" 1 "kind-e2e-cluster-3" 3

if [[ "${undeploy}" == "true" ]]; then
  undeploy "kind-e2e-cluster-1" 1 "${NAMESPACE}" &
  undeploy "kind-e2e-cluster-2" 2 "${NAMESPACE}" &
  undeploy "kind-e2e-cluster-3" 3 "${NAMESPACE}" &
fi
