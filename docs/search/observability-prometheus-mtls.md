# Secure the mongot Prometheus `/metrics` endpoint with mTLS

`MongoDBSearch` can serve the mongot Prometheus `/metrics` endpoint over HTTPS and
require scrapers to present a client certificate. It is opt-in: without
`spec.observability.prometheus.tls`, the endpoint keeps serving plain HTTP.

This is the *inbound scrape* mTLS between the mongot `/metrics` listener and its
scrapers. It is unrelated to the operator's outbound Ops Manager connection and to
the mongot gRPC/wire-protocol TLS in `spec.security.tls`.

> Minimum mongot version: `metrics.tls` first ships in **mongot 1.77.0** (the
> change landed on master 2026-09-30; the 1.77.0 branch cut is 2026-10-08). The
> operator refuses to render it for older images via `minMetricsTLSMongotVersion`.
> MCK ships the self-managed/Enterprise line, where the feature arrives with the
> "Mongot vNext" release (projected base 1.79.0, ~mid-December 2026); any such base
> is >= 1.77.0, so the same gate applies.

## Credentials

mTLS has two independent trust directions; they may share a CA but the operator
never assumes so.

| Reference | Kind | Keys | Direction |
| --- | --- | --- | --- |
| `tls.serverCertificateSecretRef` | Secret | `tls.crt`, `tls.key` | mongot server identity |
| `tls.clientCAConfigMapRef` | ConfigMap | `ca.crt` | CAs mongot trusts for scraper client certs |
| `tls.scraper.clientCertificateSecretRef` | Secret | `tls.crt`, `tls.key` | forwarder client identity |
| `tls.scraper.serverCAConfigMapRef` | ConfigMap | `ca.crt` | CAs the forwarder trusts for the mongot server cert |

- The operator combines `tls.crt` and `tls.key` from `serverCertificateSecretRef`
  into one PEM (leaf, then intermediates) and mounts it as
  `metrics.tls.certificateKeyFile`. Keys must be unencrypted.
- In multi-cluster deployments the search-side Secret/ConfigMap must exist in every
  member cluster (as with the search source TLS secrets). The forwarder reads its
  client cert and server CA from the central cluster and replicates operator-owned
  copies into each member cluster.
- Private keys never appear in a ConfigMap, status, or logs; the mongot ConfigMap
  references only mounted file paths.

## Server certificate identity (required SANs)

The forwarder discovers mongot pods via the headless Service's SRV records
(`_prometheus._tcp.<svc>`) and dials each pod FQDN. Hostname verification is
mandatory; the operator never sets `server_name` or `insecure_skip_verify`. The
server certificate must cover every mongot pod FQDN:

- Replica set: `<name>-search-<idx>-<ordinal>.<name>-search-<idx>-svc.<ns>.svc.<clusterDomain>`
- Sharded: `<name>-search-<idx>-<shard>-<ordinal>.<name>-search-<idx>-<shard>-svc.<ns>.svc.<clusterDomain>`

Because ordinals change with scaling, issue a wildcard SAN per headless Service,
for example `DNS:*.search-search-0-svc.mongodb.svc.cluster.local`, or per-pod
certificates. A certificate missing the dialed FQDN fails the handshake.

## Example

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: search
spec:
  version: "1.77.0"
  source:
    mongodbResourceRef:
      name: mdb
  observability:
    prometheus:
      mode: enabled
      tls:
        serverCertificateSecretRef:
          name: mongot-metrics-server
        clientCAConfigMapRef:
          name: mongot-metrics-client-ca
        scraper:
          clientCertificateSecretRef:
            name: metrics-forwarder-client
          serverCAConfigMapRef:
            name: mongot-metrics-server-ca
  clusters:
    - replicas: 1
```

## Validation, rotation, troubleshooting

- Rejected: `tls` with `prometheus.mode: disabled`; missing/empty references;
  missing Secret/ConfigMap keys; a mongot version below the gate. Failures surface
  in the resource status.
- When the built-in forwarder is enabled, `tls.scraper` is required. When it is
  disabled, `tls.scraper` may be omitted and external scrapers must configure HTTPS
  with the client cert and server CA themselves.
- mongot reads TLS files once at startup, so a change requires a restart. The
  operator changes the mounted server-cert path on rotation and folds the client CA
  content into the mongot config hash, so the StatefulSet rolls automatically;
  forwarder cert/CA rotation rolls the forwarder Deployment. Removing `tls`
  restores HTTP and removes only operator-owned mounts/copies.
- `certificate is valid for ... not <pod-fqdn>`: add the missing SAN.
- `certificate required`/handshake failure: the scraper presented no client cert or
  one not chaining to `clientCAConfigMapRef`.
- Server CA mismatch: `scraper.serverCAConfigMapRef` does not hold the CA that
  issued the mongot server certificate.
