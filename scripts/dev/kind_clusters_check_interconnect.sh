#!/bin/bash

set -euo pipefail

install_echo() {
    ctx=$1
    cluster_no=$2
    ns=$3

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
        - image: gcr.io/google_containers/echoserver:1.0
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
}

test_connectivity() {
  first_context=$1
  first_idx=$2
  second_context=$3
  second_idx=$4

  pod1=$(kubectl get pods --context "${first_context}" -n "${NAMESPACE}" -o name | grep "echoserver${first_idx}")
  pod2=$(kubectl get pods --context "${second_context}" -n "${NAMESPACE}" -o name | grep "echoserver${second_idx}")
  echo "Checking own service connection"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl -v http://echoserver${first_idx}.${NAMESPACE}.svc.cluster.local:8080"
  echo "Checking service connection from ${first_context} to ${second_context}"
  kubectl exec --context "${first_context}" -n "${NAMESPACE}" "${pod1}" -- /bin/bash -c "curl -v http://echoserver${second_idx}.${NAMESPACE}.svc.cluster.local:8080"
  echo "Checking service connection from ${second_context} to ${first_context}"
  kubectl exec --context "${second_context}" -n "${NAMESPACE}" "${pod2}" -- /bin/bash -c "curl -v http://echoserver${first_idx}.${NAMESPACE}.svc.cluster.local:8080"
}

wait_for_deployment() {
  ctx=$1
  idx=$2

  echo "Waiting for echoserver${idx} deployment"
  kubectl wait --context "${ctx}" -n "${NAMESPACE}" --for=condition=available --timeout=60s "deployment/echoserver${idx}"
}

undeploy() {
  ctx=$1
  idx=$2

  echo "Deleting echoserver${idx}"
  kubectl delete deployment --context "${ctx}" -n "${NAMESPACE}" "echoserver${idx}"
}

install_echo "kind-e2e-cluster-1" 1 "${NAMESPACE}" &
install_echo "kind-e2e-cluster-2" 2 "${NAMESPACE}" &
install_echo "kind-e2e-cluster-3" 3 "${NAMESPACE}" &

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

undeploy "kind-e2e-cluster-1" 1 "${NAMESPACE}" &
undeploy "kind-e2e-cluster-2" 2 "${NAMESPACE}" &
undeploy "kind-e2e-cluster-3" 3 "${NAMESPACE}" &




