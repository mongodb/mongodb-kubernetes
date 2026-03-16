#!/usr/bin/env bash
# Create TLS certificates for the managed load balancer (Envoy proxy)
# Server cert: for incoming mongod connections. Client cert: for outgoing mongot connections.

source "code_snippets/_tls_helpers.sh"

echo "Creating TLS certificates for managed load balancer (Envoy)..."

lb_server_cert="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-cert"
lb_client_cert="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-client-cert"

# Build DNS names for LB server certificate (one per shard proxy service)
lb_dns_names=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  proxy_svc="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc"
  lb_dns_names="${lb_dns_names}    - ${proxy_svc}.${MDB_NS}.svc.cluster.local
"
done
lb_dns_names="${lb_dns_names}    - \"*.${MDB_NS}.svc.cluster.local\""

echo "Creating LB server certificate..."
create_cert "${lb_server_cert}" "${lb_dns_names}"
echo "  ✓ LB server certificate requested: ${lb_server_cert}"

echo "Creating LB client certificate..."
create_cert "${lb_client_cert}" \
  "    - \"*.${MDB_NS}.svc.cluster.local\"" \
  "    - client auth"
echo "  ✓ LB client certificate requested: ${lb_client_cert}"

echo "Waiting for LB certificates to be ready..."
kubectl wait --for=condition=Ready certificate/"${lb_server_cert}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s
kubectl wait --for=condition=Ready certificate/"${lb_client_cert}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s

echo "✓ All managed load balancer (Envoy) TLS certificates created"
