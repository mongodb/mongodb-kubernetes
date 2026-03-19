#!/usr/bin/env bash
# Update CoreDNS to resolve the external domain to the mongos ClusterIP.
# This simulates external DNS resolution within the single-cluster test environment.
#
# The mongos service is created asynchronously by the operator after the MongoDB
# CR is applied, so we poll until it exists and has a ClusterIP assigned.

MONGOS_SVC="${MDB_EXTERNAL_CLUSTER_NAME}-svc"
TIMEOUT=600
INTERVAL=5
ELAPSED=0
MONGOS_CLUSTER_IP=""

echo "Waiting up to ${TIMEOUT}s for service ${MONGOS_SVC} to get a ClusterIP..."
while [[ ${ELAPSED} -lt ${TIMEOUT} ]]; do
  MONGOS_CLUSTER_IP=$(kubectl get svc "${MONGOS_SVC}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)

  if [[ -n "${MONGOS_CLUSTER_IP}" ]]; then
    echo "Service ${MONGOS_SVC} has ClusterIP: ${MONGOS_CLUSTER_IP}"
    break
  fi

  echo "  ...service not ready yet (${ELAPSED}s elapsed)"
  sleep ${INTERVAL}
  ELAPSED=$((ELAPSED + INTERVAL))
done

if [[ -z "${MONGOS_CLUSTER_IP}" ]]; then
  echo "ERROR: Timed out waiting for ClusterIP on service ${MONGOS_SVC} after ${TIMEOUT}s"
  exit 1
fi

MONGOS_EXTERNAL_HOSTNAME="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_DOMAIN}"
echo "Mapping ${MONGOS_EXTERNAL_HOSTNAME} → ${MONGOS_CLUSTER_IP} in CoreDNS"

kubectl --context "${K8S_CTX}" -n kube-system apply -f - <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns
data:
  Corefile: |
    .:53 {
        errors
        health {
           lameduck 5s
        }
        ready
        kubernetes cluster.local in-addr.arpa ip6.arpa {
           pods insecure
           fallthrough in-addr.arpa ip6.arpa
           ttl 30
        }
        prometheus :9153
        forward . /etc/resolv.conf {
           max_concurrent 1000
        }
        cache 30
        loop
        reload
        loadbalance
        hosts {
           ${MONGOS_CLUSTER_IP} ${MONGOS_EXTERNAL_HOSTNAME}
           fallthrough
        }
    }
YAML

kubectl --context "${K8S_CTX}" -n kube-system rollout restart deployment coredns
kubectl --context "${K8S_CTX}" -n kube-system rollout status deployment coredns --timeout=60s
echo "✓ CoreDNS updated: ${MONGOS_EXTERNAL_HOSTNAME} → ${MONGOS_CLUSTER_IP}"
