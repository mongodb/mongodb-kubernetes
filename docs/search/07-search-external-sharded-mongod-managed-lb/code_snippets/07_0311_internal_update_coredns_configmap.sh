MONGOS_POD="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0"
TIMEOUT=600
INTERVAL=5
ELAPSED=0
MONGOS_POD_IP=""

echo "Waiting up to ${TIMEOUT}s for pod ${MONGOS_POD} to get a PodIP..."
while [[ ${ELAPSED} -lt ${TIMEOUT} ]]; do
  MONGOS_POD_IP=$(kubectl get pod "${MONGOS_POD}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -o jsonpath='{.status.podIP}' 2>/dev/null || true)

  if [[ -n "${MONGOS_POD_IP}" && "${MONGOS_POD_IP}" != "None" ]]; then
    echo "Pod ${MONGOS_POD} has PodIP: ${MONGOS_POD_IP}"
    break
  fi

  echo "  ...pod not ready yet (${ELAPSED}s elapsed)"
  sleep ${INTERVAL}
  ELAPSED=$((ELAPSED + INTERVAL))
done

if [[ -z "${MONGOS_POD_IP}" || "${MONGOS_POD_IP}" == "None" ]]; then
  echo "ERROR: Timed out waiting for PodIP" \
    "on pod ${MONGOS_POD} after ${TIMEOUT}s"
  exit 1
fi

MONGOS_EXTERNAL_HOSTNAME=\
"${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_DOMAIN}"
echo "Mapping ${MONGOS_EXTERNAL_HOSTNAME}" \
  "-> ${MONGOS_POD_IP} in CoreDNS"

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
           ${MONGOS_POD_IP} ${MONGOS_EXTERNAL_HOSTNAME}
           fallthrough
        }
    }
YAML

kubectl --context "${K8S_CTX}" -n kube-system rollout restart deployment coredns
kubectl --context "${K8S_CTX}" -n kube-system \
  rollout status deployment coredns --timeout=60s
echo "[ok] CoreDNS updated:" \
  "${MONGOS_EXTERNAL_HOSTNAME} -> ${MONGOS_POD_IP}"
