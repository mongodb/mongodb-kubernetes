# Envoy Configuration Explained

This document explains how the Envoy proxy configuration works for SNI-based routing to multiple MongoDB replica sets.

## Traffic Flow Overview

```
mongod pod                     Envoy Proxy                    mongot pod
    |                              |                              |
    |--TLS handshake (SNI)-------->|                              |
    |  SNI: mdb-rs-1-proxy-svc...  |                              |
    |                              |--match SNI to filter chain-->|
    |                              |                              |
    |<--mTLS established---------->|                              |
    |                              |                              |
    |--gRPC request (HTTP/2)------>|                              |
    |                              |--route to cluster----------->|
    |                              |  mongot_rs1_cluster          |
    |                              |                              |
    |                              |--new TLS conn (mTLS)-------->|
    |                              |  to mdb-rs-1-search-svc      |
    |                              |                              |
    |<--gRPC response--------------|<--response-------------------|
```

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              ENVOY PROXY                                     │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    LISTENER (port 27029)                             │   │
│  │                                                                      │   │
│  │  ┌──────────────────┐                                               │   │
│  │  │  TLS Inspector   │  ← Extracts SNI from TLS ClientHello          │   │
│  │  └────────┬─────────┘                                               │   │
│  │           │                                                          │   │
│  │           ▼                                                          │   │
│  │  ┌──────────────────────────────────────────────────────────────┐   │   │
│  │  │              FILTER CHAIN SELECTION (by SNI)                  │   │   │
│  │  │                                                               │   │   │
│  │  │  SNI = mdb-rs-1-proxy-svc... → Filter Chain 1 → Cluster 1    │   │   │
│  │  │  SNI = mdb-rs-2-proxy-svc... → Filter Chain 2 → Cluster 2    │   │   │
│  │  └──────────────────────────────────────────────────────────────┘   │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────┐    ┌─────────────────────────┐               │
│  │   mongot_rs1_cluster    │    │   mongot_rs2_cluster    │               │
│  │                         │    │                         │               │
│  │  → mdb-rs-1-search-svc  │    │  → mdb-rs-2-search-svc  │               │
│  │    (headless, port 27028)│    │    (headless, port 27028)│               │
│  └─────────────────────────┘    └─────────────────────────┘               │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Configuration Breakdown

### 1. Admin Interface

```yaml
admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 9901
```

- Exposes Envoy admin API on port **9901**
- Used for:
  - Health checks (`/ready`)
  - Metrics (`/stats/prometheus`)
  - Debugging (`/config_dump`, `/clusters`, `/listeners`)

---

### 2. Listener

```yaml
listeners:
- name: mongod_listener
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 27029
```

- Single listener on port **27029** accepting all incoming connections
- This is where mongod pods connect to reach mongot
- All traffic regardless of target replica set comes to this single port

---

### 3. TLS Inspector - The SNI Routing Key

```yaml
listener_filters:
- name: envoy.filters.listener.tls_inspector
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
```

**This is critical for SNI-based routing!**

How it works:
1. Runs **before** filter chains are selected
2. Peeks at the TLS ClientHello message **without terminating TLS**
3. Extracts the **SNI (Server Name Indication)** field
4. SNI is what the client specifies as the hostname it wants to connect to
5. This extracted SNI is then used to match against `filter_chain_match.server_names`

**Why SNI?**
- SNI is sent in cleartext during TLS handshake (before encryption)
- Allows routing decisions before TLS termination
- Client sets SNI based on the DNS name it's connecting to

---

### 4. Filter Chains with SNI Matching

**Filter Chain 1 - for mdb-rs-1:**
```yaml
filter_chains:
- filter_chain_match:
    server_names:
    - "mdb-rs-1-proxy-svc.ls.svc.cluster.local"
  filters:
    # ... routes to mongot_rs1_cluster
```

**Filter Chain 2 - for mdb-rs-2:**
```yaml
- filter_chain_match:
    server_names:
    - "mdb-rs-2-proxy-svc.ls.svc.cluster.local"
  filters:
    # ... routes to mongot_rs2_cluster
```

**Selection Process:**
1. mongod connects to `mdb-rs-1-proxy-svc.ls.svc.cluster.local:27029`
2. Kubernetes DNS resolves this to the Envoy pod IP (both services point to same pod)
3. mongod initiates TLS handshake with SNI = `mdb-rs-1-proxy-svc.ls.svc.cluster.local`
4. TLS Inspector extracts this SNI
5. Envoy matches SNI against `filter_chain_match.server_names`
6. First matching filter chain is selected
7. Traffic is routed to `mongot_rs1_cluster`

---

### 5. HTTP Connection Manager

```yaml
filters:
- name: envoy.filters.network.http_connection_manager
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
    stat_prefix: ingress_rs1
    codec_type: AUTO
    route_config:
      name: rs1_route
      virtual_hosts:
      - name: mongot_rs1_backend
        domains: ["*"]
        routes:
        - match:
            prefix: "/"
            grpc: {}
          route:
            cluster: mongot_rs1_cluster
            timeout: 300s
    http_filters:
    - name: envoy.filters.http.router
    http2_protocol_options:
      initial_connection_window_size: 1048576
      initial_stream_window_size: 1048576
    stream_idle_timeout: 300s
    request_timeout: 300s
```

| Setting | Purpose |
|---------|---------|
| `codec_type: AUTO` | Auto-detects HTTP/1.1 or HTTP/2 |
| `grpc: {}` | Matches gRPC traffic (uses HTTP/2) |
| `cluster: mongot_rs1_cluster` | Forward to this upstream cluster |
| `timeout: 300s` | Max time for request/response |
| `http2_protocol_options` | Flow control window sizes for HTTP/2 |
| `stream_idle_timeout` | Close idle streams after 300s |

---

### 6. Downstream TLS (mongod → Envoy)

```yaml
transport_socket:
  name: envoy.transport_sockets.tls
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
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
            suffix: ".ls.svc.cluster.local"
      tls_params:
        tls_minimum_protocol_version: TLSv1_2
        tls_maximum_protocol_version: TLSv1_2
        cipher_suites:
        - "ECDHE-ECDSA-AES128-GCM-SHA256"
        - "ECDHE-RSA-AES128-GCM-SHA256"
        - "ECDHE-ECDSA-AES256-GCM-SHA384"
        - "ECDHE-RSA-AES256-GCM-SHA384"
      alpn_protocols:
      - "h2"
    require_client_certificate: true
```

| Setting | Purpose |
|---------|---------|
| `DownstreamTlsContext` | TLS settings for incoming (downstream) connections |
| `tls_certificates` | Envoy's server certificate (presented to mongod) |
| `validation_context` | Validates mongod's client certificate |
| `trusted_ca` | CA certificate to verify client certs |
| `match_typed_subject_alt_names` | Only accept certs with SANs ending in `.ls.svc.cluster.local` |
| `require_client_certificate: true` | **mTLS** - mongod must present a valid cert |
| `alpn_protocols: ["h2"]` | Negotiates HTTP/2 protocol |
| `tls_params` | TLS version and cipher constraints |

---

### 7. Upstream Clusters

```yaml
clusters:
- name: mongot_rs1_cluster
  type: STRICT_DNS
  lb_policy: ROUND_ROBIN
  http2_protocol_options:
    initial_connection_window_size: 1048576
    initial_stream_window_size: 1048576
  load_assignment:
    cluster_name: mongot_rs1_cluster
    endpoints:
    - lb_endpoints:
      - endpoint:
          address:
            socket_address:
              address: mdb-rs-1-search-svc.ls.svc.cluster.local
              port_value: 27028
  circuit_breakers:
    thresholds:
    - priority: DEFAULT
      max_connections: 1024
      max_pending_requests: 1024
      max_requests: 1024
      max_retries: 3
```

| Setting | Purpose |
|---------|---------|
| `type: STRICT_DNS` | Resolves DNS name and uses all returned IPs as endpoints |
| `mdb-rs-1-search-svc` | Headless K8s service - DNS returns pod IPs directly |
| `lb_policy: ROUND_ROBIN` | Distributes requests across mongot pods |
| `circuit_breakers` | Prevents cascade failures with connection limits |
| `http2_protocol_options` | HTTP/2 flow control settings |

**Why STRICT_DNS with headless service?**
- Headless service (`clusterIP: None`) returns individual pod IPs
- Envoy resolves DNS periodically and updates endpoint list
- Enables load balancing across all mongot pods

---

### 8. Upstream TLS (Envoy → mongot)

```yaml
transport_socket:
  name: envoy.transport_sockets.tls
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
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
            suffix: ".ls.svc.cluster.local"
      alpn_protocols:
      - "h2"
    sni: mdb-rs-1-search-svc.ls.svc.cluster.local
upstream_connection_options:
  tcp_keepalive:
    keepalive_time: 10
    keepalive_interval: 3
    keepalive_probes: 3
```

| Setting | Purpose |
|---------|---------|
| `UpstreamTlsContext` | TLS settings for outgoing (upstream) connections |
| `tls_certificates` | Envoy's client certificate (presented to mongot) |
| `validation_context` | Validates mongot's server certificate |
| `sni` | SNI sent to mongot during TLS handshake |
| `tcp_keepalive` | Keep connections alive with periodic probes |

---

### 9. Runtime Configuration

```yaml
layered_runtime:
  layers:
  - name: static_layer
    static_layer:
      overload:
        global_downstream_max_connections: 50000
```

- Sets maximum downstream connections to 50,000
- Prevents resource exhaustion under high load

---

## Certificate Requirements

### Server Certificate (for mongod → Envoy)
Must have SANs for all proxy service names:
- `mdb-rs-1-proxy-svc.ls.svc.cluster.local`
- `mdb-rs-2-proxy-svc.ls.svc.cluster.local`
- (or wildcard `*.ls.svc.cluster.local`)

### Client Certificate (for Envoy → mongot)
Used for mTLS with mongot services. Must be signed by the same CA that mongot trusts.

---

## Kubernetes Services

Both services point to the same Envoy pod but use different DNS names:

```yaml
# Service 1
apiVersion: v1
kind: Service
metadata:
  name: mdb-rs-1-proxy-svc
spec:
  selector:
    app: envoy-proxy  # Same selector
  ports:
  - port: 27029

---
# Service 2
apiVersion: v1
kind: Service
metadata:
  name: mdb-rs-2-proxy-svc
spec:
  selector:
    app: envoy-proxy  # Same selector
  ports:
  - port: 27029
```

When mongod connects to `mdb-rs-1-proxy-svc:27029`:
1. DNS resolves to Envoy pod IP
2. TLS handshake includes SNI = `mdb-rs-1-proxy-svc.ls.svc.cluster.local`
3. Envoy routes to `mongot_rs1_cluster` based on SNI match

---

## Adding More Replica Sets

To add support for `mdb-rs-3`:

1. **Add filter chain** in `envoy-configmap.yaml`:
```yaml
- filter_chain_match:
    server_names:
    - "mdb-rs-3-proxy-svc.ls.svc.cluster.local"
  filters:
    # ... same structure, routes to mongot_rs3_cluster
```

2. **Add cluster**:
```yaml
- name: mongot_rs3_cluster
  # ... same structure
  load_assignment:
    endpoints:
    - lb_endpoints:
      - endpoint:
          address:
            socket_address:
              address: mdb-rs-3-search-svc.ls.svc.cluster.local
              port_value: 27028
```

3. **Add service** in `proxy-services.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: mdb-rs-3-proxy-svc
spec:
  selector:
    app: envoy-proxy
  ports:
  - port: 27029
```

4. **Update certificate** (if not using wildcard) to include new SAN.
