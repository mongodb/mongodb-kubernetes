# Deploy Envoy ConfigMap with SNI-based routing configuration
#
# This ConfigMap contains the Envoy configuration that:
# 1. Listens on port 27029 for incoming gRPC connections from mongod
# 2. Uses TLS Inspector to extract SNI from TLS handshake
# 3. Routes traffic to the appropriate per-shard mongot service based on SNI
# 4. Terminates TLS from mongod and initiates new TLS to mongot (mTLS on both sides)

echo "Generating Envoy ConfigMap for ${MDB_SHARD_COUNT} shards..."

# Build filter chains for each shard
filter_chains=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${i}"
  proxy_svc="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc"
  cluster_name="mongot_${shard_name//-/_}_cluster"

  filter_chains="${filter_chains}
        - filter_chain_match:
            server_names:
            - \"${proxy_svc}.${MDB_NS}.svc.cluster.local\"
          filters:
          - name: envoy.filters.network.http_connection_manager
            typed_config:
              \"@type\": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
              stat_prefix: ingress_${shard_name//-/_}
              codec_type: AUTO
              route_config:
                name: ${shard_name}_route
                virtual_hosts:
                - name: mongot_${shard_name//-/_}_backend
                  domains: [\"*\"]
                  routes:
                  - match:
                      prefix: \"/\"
                      grpc: {}
                    route:
                      cluster: ${cluster_name}
                      timeout: 300s
              http_filters:
              - name: envoy.filters.http.router
                typed_config:
                  \"@type\": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
              http2_protocol_options:
                initial_connection_window_size: 1048576
                initial_stream_window_size: 1048576
              stream_idle_timeout: 300s
              request_timeout: 300s
          transport_socket:
            name: envoy.transport_sockets.tls
            typed_config:
              \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
              common_tls_context:
                tls_certificates:
                - certificate_chain:
                    filename: /etc/envoy/tls/server/cert.pem
                  private_key:
                    filename: /etc/envoy/tls/server/cert.pem
                validation_context:
                  trusted_ca:
                    filename: /etc/envoy/tls/ca/ca-pem
                  match_typed_subject_alt_names:
                  - san_type: DNS
                    matcher:
                      suffix: \".${MDB_NS}.svc.cluster.local\"
                tls_params:
                  tls_minimum_protocol_version: TLSv1_2
                  tls_maximum_protocol_version: TLSv1_2
                alpn_protocols:
                - \"h2\"
              require_client_certificate: true"
done

# Build clusters for each shard
clusters=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${i}"
  search_svc="${MDB_SEARCH_RESOURCE_NAME}-mongot-${shard_name}-svc"
  cluster_name="mongot_${shard_name//-/_}_cluster"

  clusters="${clusters}
      - name: ${cluster_name}
        type: STRICT_DNS
        lb_policy: ROUND_ROBIN
        http2_protocol_options:
          initial_connection_window_size: 1048576
          initial_stream_window_size: 1048576
        load_assignment:
          cluster_name: ${cluster_name}
          endpoints:
          - lb_endpoints:
            - endpoint:
                address:
                  socket_address:
                    address: ${search_svc}.${MDB_NS}.svc.cluster.local
                    port_value: 27028
        circuit_breakers:
          thresholds:
          - priority: DEFAULT
            max_connections: 1024
            max_pending_requests: 1024
            max_requests: 1024
            max_retries: 3
        transport_socket:
          name: envoy.transport_sockets.tls
          typed_config:
            \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
            common_tls_context:
              tls_certificates:
              - certificate_chain:
                  filename: /etc/envoy/tls/client/cert.pem
                private_key:
                  filename: /etc/envoy/tls/client/cert.pem
              validation_context:
                trusted_ca:
                  filename: /etc/envoy/tls/ca/ca-pem
                match_typed_subject_alt_names:
                - san_type: DNS
                  matcher:
                    suffix: \".${MDB_NS}.svc.cluster.local\"
              alpn_protocols:
              - \"h2\"
            sni: ${search_svc}.${MDB_NS}.svc.cluster.local
        upstream_connection_options:
          tcp_keepalive:
            keepalive_time: 10
            keepalive_interval: 3
            keepalive_probes: 3
        common_http_protocol_options:
          idle_timeout: 300s"
done

# Create the ConfigMap
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: envoy-config
data:
  envoy.yaml: |
    admin:
      address:
        socket_address:
          address: 0.0.0.0
          port_value: 9901

    static_resources:
      listeners:
      - name: mongod_listener
        address:
          socket_address:
            address: 0.0.0.0
            port_value: ${ENVOY_PROXY_PORT:-27029}
        listener_filters:
        - name: envoy.filters.listener.tls_inspector
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
        filter_chains:${filter_chains}

      clusters:${clusters}

    layered_runtime:
      layers:
      - name: static_layer
        static_layer:
          overload:
            global_downstream_max_connections: 50000
EOF

echo "✓ Envoy ConfigMap created with routing for ${MDB_SHARD_COUNT} shards"
