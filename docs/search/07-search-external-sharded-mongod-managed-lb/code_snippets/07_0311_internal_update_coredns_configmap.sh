#!/usr/bin/env bash
# Update CoreDNS to resolve the external domain to the mongos ClusterIP.
# This simulates external DNS resolution within the single-cluster test environment.

MONGOS_SVC="${MDB_EXTERNAL_CLUSTER_NAME}-svc"
MONGOS_CLUSTER_IP=$(kubectl get svc "${MONGOS_SVC}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.spec.clusterIP}')

if [[ -z "${MONGOS_CLUSTER_IP}" ]]; then
  echo "ERROR: Could not get ClusterIP for service ${MONGOS_SVC}"
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
echo "✓ CoreDNS updated: ${MONGOS_EXTERNAL_HOSTNAME} → ${MONGOS_CLUSTER_IP}"
