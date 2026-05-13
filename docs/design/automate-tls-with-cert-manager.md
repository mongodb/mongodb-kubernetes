# Technical Design: Automate TLS certificates with cert-manager

| Field | Value |
| --- | --- |
| Author | Maciej Karaś |
| Date | 2026-05-07 |
| Status | DRAFT |
| Jira Epic | [CLOUDP-389489](https://jira.mongodb.org/browse/CLOUDP-389489) |
| Aha Feature | ENTOPER-86 |
| PD+Scope | [doc](https://docs.google.com/document/d/1AQ7QqH0ZAtL4OzxD7VDU6YhSlZ3vY57bcAPczWbbP-k/edit) |
| Pre-TD investigations | [Certificate Inventory and Per-Issuer Feasibility](https://docs.google.com/document/d/1nB722OaeCzPomlKmlw5h1PqLbI1Lg8pWkHLDugD4iPs/edit?tab=t.0#heading=h.bbh8k1ywuwsz), [CertificateRef field shape](https://docs.google.com/document/d/1nB722OaeCzPomlKmlw5h1PqLbI1Lg8pWkHLDugD4iPs/edit?tab=t.0#heading=h.adtf3f8jo3h5) |
| Prior SPIKE | [doc](https://docs.google.com/document/d/1-eA4ZZRnqLkvTsgo6X4WoEt2_fIZqHBRHz4gLdyb2_0/edit) |

## Existing Implementation Overview

Today, MCK requires users to manually create, distribute, and rotate TLS certificates for every MongoDB deployment. A single shared `kubernetes.io/tls` Secret contains the leaf cert and private key with a SAN list covering every member's hostname (or DNS wildcards). The operator validates SAN coverage, aggregates the leaf into a combined `-pem` Secret with previous-certificate slot for rotation, mounts that into pods, updates the Automation Config, and the agent performs sequential `mongod` restarts. Multi-cluster (`MongoDBMultiCluster`) replicates the user-provided Secret from the central cluster into each member cluster.

CA bundles are user-provided ConfigMaps consumed by mongod / OM / AppDB processes for trust validation:

- `MongoDB.spec.security.tls.ca` — ConfigMap key `ca-pem`, validates server certs.
- `MongoDBOpsManager.spec.security.tls.ca` — ConfigMap key `mms-ca.crt`, validates OM TLS for the AppDB monitoring agent.
- `MongoDBOpsManager.spec.applicationDatabase.security.tls.ca` — ConfigMap key `ca-pem`, validates AppDB TLS for OM and the agent.

### Key code anchors (master)

| Concern | Path |
| --- | --- |
| `TLSConfig` type + `AdditionalCertificateDomains` + `CA` | `api/v1/mdb/mongodb_types.go:1151,1159,1163` |
| `MemberCertificateSecretName` | `api/v1/mdb/mongodb_types.go:770` |
| `AgentClientCertificateSecretName` | `api/v1/mdb/mongodb_types.go:864` |
| `InternalClusterAuthSecretName` | `api/v1/mdb/mongodb_types.go:883` |
| Authentication.InternalCluster (X509 case) | `api/v1/mdb/mongodb_types.go:923-944` |
| `KmipClientConfig` | `api/v1/kmip.go:23-47` |
| OM TLS / `GetOpsManagerCA` / `GetAppDbCA` | `api/v1/om/opsmanager_types.go:248-289` |
| Search TLS | `api/v1/search/mongodbsearch_types.go:230-246` |
| SAN composition | `controllers/operator/certs/certificates.go:142,219,225` + `cert_configurations.go:127` |
| PEM aggregation | `controllers/operator/certs/certificates.go:43`; PEM helpers `controllers/operator/pem/pem_collection.go:106-125` |
| Subject extraction (today) | `controllers/operator/common_controller.go:497-514` + `controllers/operator/authentication/pkix.go:55-78` |
| Multi-cluster Secret replication | `controllers/operator/mongodbmultireplicaset_controller.go:443-456,460` |
| Multi-cluster CA replication | `controllers/operator/mongodbopsmanager_controller.go:1271-1318,1328-1353` |

### Pain points (motivation)

- Frequent Secret updates on scale, shard, cluster addition, or hostname changes.
- Any update forces sequential `mongod` restarts across all members (primary elections).
- Short-lived certificates impractical due to restart cadence.
- Multi-cluster requires copying Secrets between clusters.
- Inconsistent with Atlas (per-process certs, automatic rotation).

## API Contract

### CertificateRef Go type

Single uniform shape, embedded at eight sites. User-settable fields, operator-managed fields, deferred fields.

```go
package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CertificateRef configures cert-manager-driven TLS certificate provisioning
// for an MCK-managed resource. Fields not listed here are operator-managed
// (see docs/cert-manager/) or intentionally deferred until customer demand.
type CertificateRef struct {
    // IssuerRef selects the cert-manager Issuer or ClusterIssuer.
    // Cross-namespace references are not allowed.
    // Must match the operator-level allow-list when configured.
    // +kubebuilder:validation:Required
    IssuerRef IssuerRef `json:"issuerRef"`

    // Duration is the requested certificate lifetime.
    // Must be >= 1h. cert-manager defaults to 2160h (90d) when unset.
    // +optional
    Duration *metav1.Duration `json:"duration,omitempty"`

    // RenewBefore specifies how long before expiry renewal should begin.
    // Must be < Duration. cert-manager defaults to 1/3 of Duration when unset.
    // +optional
    RenewBefore *metav1.Duration `json:"renewBefore,omitempty"`

    // Subject sets RFC 5280 X.509 subject attributes on the issued certificate.
    // CommonName is intentionally NOT exposed; operator-derived where it matters.
    // Ignored at the internal-cluster-auth embedding site (operator-computed
    // deterministically there; user values rejected by validator).
    // +optional
    Subject *X509Subject `json:"subject,omitempty"`

    // PrivateKey controls the asymmetric key used in the certificate.
    // Algorithm/Size are user-settable to support FIPS profiles and ECDSA-only CAs.
    // Encoding (PKCS8) and RotationPolicy (Always) are operator-pinned.
    // +optional
    PrivateKey *CertificatePrivateKey `json:"privateKey,omitempty"`
}

// IssuerRef references a cert-manager Issuer or ClusterIssuer.
// No Namespace field by design (cross-namespace forbidden; Issuer must live
// in the same namespace as the CR; ClusterIssuer is cluster-scoped).
type IssuerRef struct {
    // +kubebuilder:validation:Required
    Name string `json:"name"`

    // +kubebuilder:validation:Enum=Issuer;ClusterIssuer
    // +kubebuilder:default=Issuer
    // +optional
    Kind string `json:"kind,omitempty"`

    // +kubebuilder:default=cert-manager.io
    // +optional
    Group string `json:"group,omitempty"`
}

// X509Subject mirrors the user-settable subset of cert-manager.io/v1.X509Subject.
// SerialNumber is excluded — cert-manager assigns it.
type X509Subject struct {
    // +optional
    Organizations []string `json:"organizations,omitempty"`
    // +optional
    OrganizationalUnits []string `json:"organizationalUnits,omitempty"`
    // +optional
    Countries []string `json:"countries,omitempty"`
    // +optional
    Localities []string `json:"localities,omitempty"`
    // +optional
    Provinces []string `json:"provinces,omitempty"`
    // +optional
    StreetAddresses []string `json:"streetAddresses,omitempty"`
    // +optional
    PostalCodes []string `json:"postalCodes,omitempty"`
}

type CertificatePrivateKey struct {
    // +kubebuilder:validation:Enum=RSA;ECDSA;Ed25519
    // +optional
    Algorithm string `json:"algorithm,omitempty"`

    // RSA: 2048-4096; ECDSA: 256/384/521; Ed25519 ignores Size.
    // +optional
    Size *int32 `json:"size,omitempty"`
}
```

### Embedding sites

| Site | Cert type | Manual counterpart (mutual exclusion) |
| --- | --- | --- |
| `MongoDB | MongoDBMultiCluster.spec.security.tls.certificateRef` (shared `TLSConfig`) | server | `spec.security.certsSecretPrefix` |
| `MongoDBOpsManager.spec.security.tls.certificateRef` | OM server | any of: `spec.security.tls.secretRef`, `spec.security.certsSecretPrefix` set ⇒ reject |
| `MongoDBOpsManager.spec.applicationDatabase.security.tls.certificateRef` | AppDB server | `spec.applicationDatabase.security.certsSecretPrefix` |
| `MongoDBSearch.spec.security.tls.certificateRef` | Search/mongot server | `spec.security.tls.certificateKeySecretRef` |
| `spec.security.authentication.agents.clientCertificateRef` | agent client | `spec.security.authentication.agents.clientCertificateSecretRef` |
| `spec.security.authentication.internalClusterCertificateRef` | internal cluster auth (dual-EKU) | `spec.security.certsSecretPrefix` (clusterfile suffix) |
| `spec.backup.encryption.kmip.client.certificateRef` | KMIP client | `spec.backup.encryption.kmip.client.clientCertificatePrefix` |
| `spec.prometheus.certificateRef` (on `MongoDB`, `MongoDBMultiCluster`, `MongoDBOpsManager`) | Prometheus scrape endpoint server | `spec.prometheus.tlsSecretKeyRef` |

### Per-cert-type EKU rules (operator-managed)

| Cert site | EKU | Notes |
| --- | --- | --- |
| MongoDB / AppDB / OM / Search server | `serverAuth` | SANs from existing helpers (see Algorithm(s) — SAN composition). |
| AppDB server when `Authentication.InternalCluster == X509` | `serverAuth` + `clientAuth` | Same single cert serves both roles. |
| Prometheus server | `serverAuth` | Scrape endpoint hostname only. |
| Agent client | `clientAuth` | CN = operator-derived from `spec.security.authentication.agents.automationUserName`. |
| Internal cluster auth | `serverAuth` + `clientAuth` | Deterministic Subject (operator-computed; user override rejected). |
| KMIP client | `clientAuth` | CN user-supplied via `Subject.commonName`-equivalent; mandatory. |

### Operator-managed fields (not exposed on `CertificateRef`)

**Operator-computed at issuance time** (operator sets these on the cert-manager `Certificate.spec`):

- `dnsNames` — computed from SAN composition (Algorithm(s) — SAN composition).
- `secretName` — derived from the embedding-site naming helper (e.g., `MemberCertificateSecretName`).
- `usages` — per-cert-type fixed set (EKU rules above).
- `commonName` — computed where it matters: agent client cert (CN = `automationUserName`); KMIP client cert (CN from user-provided `Subject.commonName` field — KMIP-only; the rest never carry CN).
- `secretTemplate.labels` — operator-set labels: `mongodb.com/managed-by`, `mongodb.com/managed-mode`, `mongodb.com/owner`.

**Pinned to fixed values** (operator sets these unconditionally):

- `isCA` — `false`.
- `keystores` — empty (mongod is PEM-only).
- `additionalOutputFormats` — empty (operator does its own PEM aggregation).
- `revisionHistoryLimit` — `1`.
- `encodeUsagesInRequest` — default (`true`).
- `privateKey.encoding` — `PKCS8`.
- `privateKey.rotationPolicy` — `Always`.

**Never exposed and never set by operator**: `literalSubject` (escape hatch users can use today via the manual `certsSecretPrefix` flow if they need raw DN control).

### Deferred fields (not in v1; revisit on customer demand)

`uris`, `otherNames`, `secretTemplate`, `signatureAlgorithm`, `renewBeforePercentage`. No `certSpecOverride` free-form escape hatch in v1 (Sketchpad §9.1; ship opinionated, add overrides only on demand).

### CEL validations

Each embedding site carries its own CEL rule. Two exemplar shapes:

```cel
// Shape 1: secret-ref-style mutual exclusion (most sites, e.g., MongoDB server site)
//   "any manual TLS Secret reference set ⇒ certificateRef must be unset"
self.security.tls.certificateRef == null || (
  !has(self.security.certsSecretPrefix) || self.security.certsSecretPrefix == ''
)

// Shape 2: prefix-style mutual exclusion (KMIP client site)
//   "clientCertificatePrefix and certificateRef are mutually exclusive"
self.backup.encryption.kmip.client.certificateRef == null || (
  !has(self.backup.encryption.kmip.client.clientCertificatePrefix) ||
  self.backup.encryption.kmip.client.clientCertificatePrefix == ''
)
```

(Final CEL strings are locked during CRD-generation work; the shapes above are illustrative, not normative.)

Additional rules:

- `IssuerRef.Kind` enum (handled by kubebuilder marker; CEL not needed).
- `Subject` rejected when set on `internalClusterCertificateRef` (operator-computed deterministically; user override breaks cluster auth).
- No `x-kubernetes-preserve-unknown-fields` anywhere in the new types.

### Helm values

```yaml
certManager:
  enabled: false                   # default; gates RBAC, watches, controllers
  allowedIssuers: []               # optional; entries: {name, kind, namespace?}
                                   # Issuer entries match same-namespace CRs only
                                   # ClusterIssuer entries have no namespace constraint
```

CRD always installed regardless of `enabled`. Optional RBAC template `operator-roles-certmanager.yaml` rendered only when `enabled=true`.

### RBAC

Cluster role additions (rendered when `certManager.enabled=true`):

```
apiGroups: ["cert-manager.io"]
resources: ["certificates"]
verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

No `Issuer` / `ClusterIssuer` access (operator never GETs them).

## Algorithm(s)

### SAN composition

Reuses today's helpers. For each Certificate covering N members:

1. `dns.GetDNSNames(ResourceName, ServiceName, Namespace, ClusterDomain, Replicas, nil)` — per-pod cluster-internal FQDNs.
2. `<podName>.<additionalCertificateDomains[i]>` for each member ordinal — per `controllers/operator/certs/certificates.go:225` `GetAdditionalCertDomainsForMember`.
3. `<podName>.<externalDomain>` per member when `spec.externalAccess.externalDomain` is set, multi-cluster equivalent for `MongoDBMultiCluster`.
4. `replicaSetHorizons[member]` hostnames per member (parsed via `url.URL{Host: ...}.Hostname()`).

The cert-manager Certificate Builder feeds the union into `Certificate.spec.dnsNames`. No user override path. Phase 1 ships one Certificate per cert-type per resource (per-deployment mode); per-pod is a Future Work item.

### Internal-cluster-auth Subject (deterministic)

Today's flow extracts the Subject DN from a user-supplied cert via `getSubjectFromCertificate` (`controllers/operator/common_controller.go:497-514`) using `authentication.GetCertificateSubject` (`controllers/operator/authentication/pkix.go:55-78`). For cert-manager-driven issuance, the operator computes the Subject and writes it into `Certificate.spec.subject` + `Certificate.spec.commonName`.

Subject template (proposed; final form locked during implementation):

```
O=<resource-name>-cluster, OU=<namespace>, CN=<resource-name>-internal
```

Hoisted into `controllers/operator/certs/cert_configurations.go` as `Options.ClusterAuthSubject`. Required determinism unit test: round-trip operator-encoded Subject → cert-manager-issued cert → `GetCertificateSubject` extraction → byte-equal to operator's encoded Subject. The existing RFC-4514 encoder is RDN-order-sensitive (`pkix.go:96`); the test must guard against accidental ordering drift.

### IssuerType classification — N/A

Out of scope. Operator does not classify issuer types. See Design — Validation strategy and Design Alternatives — A4.

### CA-bundle assembly

```go
// assembleCABundle returns a deduplicated PEM concatenation of the input CAs,
// the list of clusters whose contribution was skipped (empty ca.crt or
// tls.crt == ca.crt defensive case), and any error.
//
// Phase 1 always passes len(perClusterBlobs) == 1 (single central cluster).
// A Future-Work per-cluster mode would pass one entry per member cluster.
func assembleCABundle(perClusterBlobs map[string][]byte) (out []byte, skipped []string, err error)
```

Algorithm:

1. For each blob: walk `pem.Decode` (existing pattern at `controllers/operator/pem/pem_collection.go:106-125`).
2. For each block where `Type == "CERTIFICATE"`: SHA256(block.Bytes) → dedup set.
3. Skip the entire blob when `tls.crt == ca.crt` (SelfSigned issuer or any issuer that copies the leaf into both keys).
4. Emit `pem.EncodeToMemory` over the dedup set in deterministic order (sorted by SHA hex).
5. Return skipped cluster keys for the `CACertificateAvailable=False` reason message.

Existing PEM decode utilities cover step 1; concat + dedup is net-new (~50 LOC + table tests).

### Watch predicate

Operator sets three labels on `Certificate.spec.secretTemplate.labels`:

```
mongodb.com/managed-by: mongodb-kubernetes-operator   # used by the watch predicate
mongodb.com/managed-mode: central                     # informational; forward-compat (b)
mongodb.com/owner: <cr-name>                          # informational; aids debugging + correlation
```

cert-manager propagates these labels to the issued Secret. The operator's `Secret` watch uses a single label predicate matching **only** `mongodb.com/managed-by=mongodb-kubernetes-operator`. The other two labels are informational — present for human operators and Future-Work cleanup queries, not consumed by the watch predicate. Reliable independent of cert-manager's `--enable-certificate-owner-ref` flag.

### Renewal flow

1. cert-manager updates the Secret (resource-version bump + content change).
2. Operator's labeled Secret watch fires.
3. Operator runs `CreateOrUpdatePEMSecretWithPreviousCert` — combined PEM with previous-cert slot.
4. Operator updates Automation Config; agent issues sequential `mongod` restarts.
5. No agent `rotateCertificates` dependency (design must not block future agent support).

### Reissuance triggers

The operator updates `Certificate.spec.dnsNames` (and re-applies the Certificate) when:

- Member count changes (scale up/down).
- `spec.externalAccess.externalDomain` changes.
- `spec.connectivity.replicaSetHorizons` changes.
- `spec.security.tls.additionalCertificateDomains` changes.

cert-manager reissues; the renewal flow above takes over. Risk of silent SAN mismatch if the operator did not trigger reissuance — preferred: take the rolling-restart over a silent failure.

### CR delete cleanup

1. Reconciler observes deletion timestamp.
2. Operator deletes its `Certificate` resources (cmv1).
3. Operator explicitly deletes the resulting Secrets by name (idempotent: NotFound → no-op). Robust regardless of cert-manager's `--enable-certificate-owner-ref` setting.
4. Operator-owned CA ConfigMap is GC'd via owner-ref (central cluster) and via label-driven cleanup in member clusters (replicator path). See Design — CA-bundle distribution.

## Design

### High-level architecture

When the user adds a `certificateRef` block to a CR (and the operator-level Helm flag is on), the operator:

1. Constructs a `cert-manager.io/v1.Certificate` resource from the `CertificateRef` plus per-cert-type context (Subject, EKU, SAN list).
2. Watches `Certificate` and downstream `Secret` resources, filtered by an operator-set label.
3. Once cert-manager issues the Secret, the existing PEM-aggregation / mount / Automation-Config pipeline takes over unchanged.
4. Best-effort copies `ca.crt` from the issued Secret into an operator-owned ConfigMap and auto-fills the relevant `tls.ca` field on the CR if the user did not set it themselves.
5. On `replicaSetHorizons` or external-domain changes, updates `Certificate.spec.dnsNames`; cert-manager reissues; pipeline continues.
6. On CR delete, deletes both the `Certificate` and the issued Secret (idempotent, regardless of cert-manager's owner-ref flag).
7. Surfaces provisioning state via `CertificateProvisioned`, `CertificateError`, and `CACertificateAvailable` conditions on the CR.

### Components

**Vendored upstream API (only):**

```
github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1
github.com/cert-manager/cert-manager/pkg/apis/meta/v1     // for cmmeta.IssuerReference (used by cmv1.CertificateSpec.IssuerRef)
```

No other cert-manager imports. cert-manager's API types do not appear on any MCK CRD surface.

**New package: `controllers/operator/certs/certmanager/`**

- `Builder`: constructs `cmv1.Certificate` from `CertificateRef` + cert-type context (server / agent-client / cluster-auth / KMIP). Selects EKU, Subject, SANs, secretName, label set.
- Cert-type-specific Subject derivation. Internal-cluster-auth Subject is operator-computed deterministically from CR identity (no user override) and lives on `Options.ClusterAuthSubject` in `controllers/operator/certs/cert_configurations.go`.
- `assembleCABundle`: helper that takes a map of per-cluster `ca.crt` blobs and returns a deduplicated PEM concatenation. Phase 1 always passes a single-entry map; Future Work (per-cluster mode) reuses the helper with N entries.
- Validators (admission-time): name-based allow-list, `IssuerRef.Kind` enum, per-site mutual exclusion with manual cert refs.

**Operator wiring:**

- Helm value `certManager.enabled: bool` (default `false`). Gates RBAC template rendering, watch registration, controller startup paths.
- Helm value `certManager.allowedIssuers: list` (optional). Outer name-based admission gate.
- Optional Helm RBAC template `operator-roles-certmanager.yaml`, mirroring `operator-roles-pvc-resize.yaml`. Permissions: CRUD on `certificates.cert-manager.io` only. Operator does **not** GET `Issuer` / `ClusterIssuer`.
- CRD always installed regardless of flag; new fields are no-op when off.

**Reconciler integration** in existing controllers (`mongodbreplicaset`, `mongodbshardedcluster`, `mongodbmulticluster`, `mongodbstandalone`, `appdbreplicaset`, `mongodbopsmanager`, `mongodbsearch`):

- Pre-flight: detect cert-manager CRDs at startup. If absent and `certManager.enabled=true`, log warning, emit cluster-level metric, continue. Per-CR validation rejects `certificateRef` with `CertificateError=True, Reason=CertManagerCRDsMissing`.
- When `certificateRef` is set: ensure `Certificate` exists / matches desired spec, watch the resulting Secret, requeue until Ready.
- On Ready: existing PEM aggregation + mount + AC update pipeline.
- Best-effort `ca.crt` distribution per resource (MongoDB, OM, AppDB).

### Data flow (happy path, single-cluster)

```
User applies CR with certificateRef
        |
        v
+----------------------+
| Validating webhook   |  --> reject if {Kind not in enum, allowed-list miss,
| (extends existing)   |       cross-namespace, manual-secret + certificateRef both set}
+----------+-----------+
           |
           v
+----------------------+
| Reconciler           |  --> creates cmv1.Certificate (label mongodb.com/managed-by,
|                      |       managed-mode=central, owner: <cr>)
+----------+-----------+
           |
           v
+----------------------+
| cert-manager         |  --> creates kubernetes.io/tls Secret (label propagated
|                      |       via Certificate.spec.secretTemplate.labels)
+----------+-----------+
           |
           v
+----------------------+
| Operator Secret-watch|  --> label predicate filters to ours
| (label-based filter) |
+----------+-----------+
           |
           v
+----------------------+
| Existing pipeline    |  --> CreateOrUpdatePEMSecretWithPreviousCert
| (PEM + mount + AC)   |       mount in StatefulSet
|                      |       update Automation Config
|                      |       agent performs sequential mongod restarts
+----------+-----------+
           |
           v
+----------------------+
| Best-effort CA copy  |  --> assembleCABundle; write to <resource>-cert-manager-ca CM
|                      |       auto-fill spec.security.tls.ca only if unset
|                      |       skip when SelfSigned or tls.crt == ca.crt
+----------+-----------+
           |
           v
+----------------------+
| Status conditions    |  CertificateProvisioned=True
|                      |  CACertificateAvailable={True | False (with reason) | Unknown}
+----------------------+
```

### Validation strategy

| Layer | Checks | Notes |
| --- | --- | --- |
| CEL XValidation on CRD | `IssuerRef.Kind` enum (`Issuer` | `ClusterIssuer`); per-site mutual exclusion (e.g., `certificateRef` vs `certsSecretPrefix`, KMIP uses `clientCertificatePrefix` not a secret-ref so its rule is shaped accordingly) | No cross-resource lookups; no `x-kubernetes-preserve-unknown-fields`. |
| Validating webhook (existing) | Allow-list match (`certManager.allowedIssuers`), no-cross-namespace `IssuerRef`, all CEL constraints replicated for old-K8s clusters where CEL is unreliable | Extends existing operator validating webhook. No new webhook endpoint. |
| Reconcile-time | `Issuer` exists in same namespace (when `Kind=Issuer`); cert-manager CRDs present; allow-list still valid (Issuer mutated post-admission) | Surfaces via `CertificateError` status condition. |
| Runtime (cert-manager) | All issuer-type-specific behavior — ACME EKU rejection, Vault PKI role denial, AWS PCA template mismatch, etc. | Operator does not classify issuer types. Failures propagate verbatim from `CertificateRequest.status.conditions`. |

**No issuer-type classification in Phase 1.** No `IssuerType()` helper, no GET on `Issuer` / `ClusterIssuer`, no per-(cert-type, issuer-type) admission rules. Per-issuer compatibility constraints (ACME-public clientAuth EKU sunset, ACME cluster-internal DNS limitation, KMIP CA pre-trust, SelfSigned + CSI exclusion) are documented in Public Docs only.

### Hard reviewer constraints

These are immutable and reflected throughout the design.

1. **No `x-kubernetes-preserve-unknown-fields`** on certificate config (Sergiusz Urbaniak, SPIKE).
2. **No vendoring or wrapping `cmv1.CertificateSpec`** in MCK CRDs. Only `pkg/apis` types vendored for internal Certificate construction. cert-manager's own guidance ([importing](https://cert-manager.io/docs/contributing/importing/)) limits public imports to `pkg/apis`.
3. **No cross-namespace references**, anywhere. `IssuerRef` has no `Namespace` field. `Issuer` (when `Kind=Issuer`) must reside in the same namespace as the CR. ConfigMap/Secret references stay namespace-local.
4. **Operator-level allow-list of issuers** (confused-deputy mitigation, Knative pattern). When configured, every `IssuerRef` must match an entry; when empty, any issuer is accepted (PD wording).

### CA-bundle distribution

#### Scope

Best-effort copy of `ca.crt` from operator-owned cert-manager Secrets into operator-owned ConfigMaps, three sites:

| Cert site | ConfigMap key | Spec field auto-fill |
| --- | --- | --- |
| MongoDB server | `ca-pem` | `spec.security.tls.ca` |
| OM server (consumed by AppDB monitoring agent) | `mms-ca.crt` | `spec.security.tls.ca` (on `MongoDBOpsManager`) |
| AppDB server | `ca-pem` | `spec.applicationDatabase.security.tls.ca` |

#### Helper shape

Single helper, three call sites, two key aliases. Underlying `assembleCABundle` (Algorithm(s) — CA-bundle assembly) is shared; each call site supplies its own consumer key alias.

#### Auto-fill rule

Operator writes the spec field (`tls.ca`) only when the user did not set it themselves. If the user set it, operator never overrides. Spec mutation is the chosen approach (vs. status-only) so the existing reader code (`mongodbreplicaset_controller.go:738`, `monitoring_tls.go:69`) continues to read from a single source.

"User did not set it" is defined as: at observe-time on this reconcile pass, the field is empty (`""`). The operator does not consult `metadata.managedFields` / field-manager ownership — once it has written a value, the operator owns it for subsequent reconciliations and may overwrite its own previously-written value when the source `ca.crt` changes (renewal, CA rotation). If a user later sets the field manually, the next reconcile observes a non-empty value and stops touching it.

#### ConfigMap shape and ownership

- Name: `<resource>-cert-manager-ca` (MongoDB, OM); `<om-name>-db-cert-manager-ca` (AppDB) — mirrors existing AppDB naming.
- Owner-ref to the parent CR in the central cluster → cascades on CR delete.
- Member-cluster copies: created via existing replicator paths (`replicateConfigMapInMemberClusters` `controllers/operator/mongodbopsmanager_controller.go:1328-1353`; `mongodbmultireplicaset_controller.go:443-456`); labelled `mongodb.com/owner=<cr-name>`; cleanup driven by replicator teardown (no cross-cluster owner-ref — Kubernetes prohibits cross-cluster owner refs).

#### Skip rules

Operator skips auto-fill when:

- `ca.crt` is empty or absent on the cert-manager Secret (SelfSigned issuer, Vault PKI role without chain).
- `tls.crt == ca.crt` defensive case (SelfSigned or any issuer that puts leaf into both).

In either case: `CACertificateAvailable=False, Reason=NoCAEmittedFromIssuer, Message="cert-manager Secret <ns>/<name> does not contain ca.crt. Set spec.security.tls.ca to a ConfigMap containing your trusted CA chain to enable mutual TLS / X.509 cluster auth."`

#### Failure handling

CA-bundle creation failure does **not** fail the resource overall. Status condition only. Downstream validation rules (e.g., X.509 cluster auth requires CA) continue to surface their own failures via existing paths.

#### Lazy cleanup

When the user removes `certificateRef` or sets `spec.security.tls.ca` themselves (overriding auto-fill), the operator removes its owned ConfigMap on the next reconcile after the spec change is observed — not in-flight. Ensures pods complete any rollout that was already underway before the CM disappears.

#### Out of scope

- Full CA issuance automation (operator never asks cert-manager to issue a CA).
- CA private-key handling.
- CA rotation logic.
- LDAP CA, KMIP server CA, OM server CA from Issuer secret — user-provided ConfigMaps remain unchanged.
- Trust-manager dependency.
- JKS/PKCS12 truststore generation. OM and AppDB consume CA as raw PEM; customers using JKS via `spec.opsManager.jvmParams` keep their existing manual flow.

### Status reporting

Single combined conditions per CR (idiomatic, short; per-cert-type split deferred if user demand surfaces).

| Condition Type | Meaning | Reasons |
| --- | --- | --- |
| `CertificateProvisioned` | True when *all* configured cert-manager-driven certs for the CR are issued, mounted, and reflected in AC | `Pending`, `Issuing`, `Provisioned`, `Renewing` |
| `CertificateError` | True when any cert-type has a hard error (admission, runtime, cert-manager `CertificateRequest` failure) | `CertManagerCRDsMissing`, `IssuerNotFound`, `IssuerDisallowed`, `CertificateRequestFailed`, `CertificateRefInvalid` (post-admission Issuer mutation), etc. |
| `CACertificateAvailable` | True when operator-owned CA ConfigMap has been published with non-empty CA chain | `Provisioned`, `NoCAEmittedFromIssuer`, `UserManaged` (user set `tls.ca` themselves), `Unknown` |

Failing-cert-type identified in the condition `Message`, plus the cert-manager `CertificateRequest.status` verbatim where applicable. Message format is **free-form, not API contract** — documented as such; subject to change between versions.

Error-message format for cert-manager runtime failures:

```
cert-manager CertificateRequest <namespace>/<name> failed: <verbatim message>.
See <Public Docs URL — TBD; tracked in Open Questions> for issuer compatibility constraints.
```

The Public Docs URL is finalized as part of the documentation ticket (Tickets — Documentation); the operator code uses a constant that the docs ticket updates before GA.

### Multi-cluster

Phase 1 multi-cluster mode: cert-manager runs in the central cluster only. The operator creates `Certificate` resources in the central cluster; the resulting Secrets are replicated to each member cluster via the existing path (`controllers/operator/mongodbmultireplicaset_controller.go:443-456,460`). Same security posture as today's user-provided Secret flow.

CA ConfigMaps follow the existing multi-cluster CA replication path (`mongodbopsmanager_controller.go:1271-1318,1328-1353`) — no new replication code in Phase 1. Forward-compat (a) below tightens the update semantics on these helpers.

Per-cluster cert-manager mode (each cluster issues locally; no cross-cluster Secret replication) is described under Future Work — Per-cluster cert-manager mode.

### Phase-2 forward-compatibility (in Phase 1 scope)

Two cheap items shipped in Phase 1 to make a future per-cluster mode purely additive rather than a partial refactor.

#### (a) Explicit "update on content change" semantics on CA replicators

`configmap.CreateOrUpdate` calls at `controllers/operator/mongodbopsmanager_controller.go:1347` and `mongodbmultireplicaset_controller.go:451` already update on content change in practice (the `IsAlreadyExists` swallow is defensive belt-and-braces). Tightening: replace the swallow with an explicit content-hash comparison + targeted update + tests. A future per-cluster multi-root union assembly depends on this update path being well-tested.

#### (b) `mongodb.com/managed-mode` label on operator-created `Certificate` resources

Emitted from day one. Phase 1 only legal value: `central`. A future per-cluster mode introduces `per-cluster`. Cleanup queries for the eventual mode switch already work correctly across both eras without retrofitting.

(An earlier "TODO-annotate single-cluster dual-client call sites" forward-compat item was considered and dropped — Phase-2 TODOs in Phase-1 code are not desired.)

## Design Alternatives

### A1: Separate `MongoDBCertificate` CRD — REJECTED

SPIKE Option 2 / Option 3 originally recommended a dedicated CRD referenced from MongoDB resources, motivated by reusability. Rejected per PD+Scope and CertificateRef-shape sketchpad §7: limited reusability for v1 (different CR types have different EKU, SAN, and Subject derivations); embedding is simpler; the embedded approach does not preclude a future CRD if reuse demand emerges.

### A2: cert-manager CSI driver — DEFERRED to Future Work

SPIKE Option 2. Mounts certificates directly into pods, no intermediate Secret. Doesn't fit the existing AC-driven rotation flow (no Secret to detect renewal on); debugging harder (cert exists only during pod lifetime); SelfSigned issuers explicitly unsupported by cert-manager-csi-driver. Phase 1 design must not preclude future CSI adoption.

### A3: Per-pod / per-process certificates — DEFERRED to Future Work

SPIKE §"Splitting certificates" + reviewer comments. Four sub-options surfaced (spoditor-style webhook, custom mutating webhook, uber-PEM with per-ordinal entries, sidecar from bundle). Lukasz/Lucian conclusion: per-pod doesn't help renewal restart cadence until the agent supports `rotateCertificates`. PD+Scope defers. Phase 1 ships per-deployment / "uber" Secret only.

### A4: Issuer-type-aware admission validation — DEFERRED to Public Docs

Per-issuer feasibility matrix (ACME-public / SelfSigned / CA / Vault / Venafi / External) is real and load-bearing for users. Inventory sketchpad §3 details it; Inventory §9.9 specifically called for an `IssuerType` helper. Decision in this TD: do **not** encode it in operator code — would require Issuer-resource GET, an `IssuerType` classifier, ongoing maintenance, and additional RBAC. Failures surface at runtime via `CertificateRequest.status` and propagate to the CR's `CertificateError` condition. The matrix moves to Public Docs as user guidance. This is a deliberate descope of Inventory §9.9.

### A5: Per-cluster cert-manager mode — DEFERRED to Future Work

Operator-level toggle for "cert-manager runs in every member cluster; certificates issued locally per cluster; no cross-cluster Secret replication." Effort: ~9 EW. Out of Phase 1 scope; described under Future Work — Per-cluster cert-manager mode.

### A6: Best-effort CA-bundle distribution — RE-SCOPED IN

PD+Scope marked CA automation as out of scope. The TD narrows that to "out of scope for full CA-issuance automation; **in scope for best-effort `ca.crt` copy** from cert-manager-issued Secrets." See Design — CA-bundle distribution for mechanics. No CA private-key handling, no rotation logic.

## Testing & QA

### Unit tests

- Builder: produces correct `cmv1.Certificate` from each `CertificateRef` shape and cert-type context.
- Per-site validators: site-specific CEL rules; mutual exclusion; allow-list match; cross-namespace rejection.
- Subject determinism: round-trip with cert-manager-encoded ASN.1 (Algorithm(s) — Internal-cluster-auth Subject).
- `assembleCABundle`: PEM dedup, SelfSigned skip, empty-blob skip, deterministic ordering.
- Watch predicate: label match true/false cases.
- CA replicator update semantics: content-hash detection + targeted update (forward-compat (a) under Design — Phase-2 forward-compatibility).
- `IssuerRef.Kind` enum + default behavior.

### E2E tests

Two Evergreen variants: cert-manager v1.13.0 (pinned floor; ~3 years old at GA — comfortable distance behind latest stable while keeping `cmv1` API surface stability we depend on) and cert-manager latest stable at release time. Same test set on both:

- SelfSigned + CA Issuer happy path (per cert type: server, agent client, internal-cluster-auth, KMIP).
- ACME via Let's Encrypt staging on `externalDomain` (server cert).
- Renewal: time-shrunk duration → cert-manager reissues → mongod sequential restarts → no data loss.
- Scale up/down: SAN list grows/shrinks → cert reissued.
- `replicaSetHorizons` change: SAN list updates → cert reissued.
- Multi-cluster Secret replication on renewal.
- CA-bundle auto-fill happy path (`CACertificateAvailable=True`).
- CA-bundle skip on SelfSigned (`CACertificateAvailable=False, Reason=NoCAEmittedFromIssuer`).
- CA-bundle user override preserved (`CACertificateAvailable=False, Reason=UserManaged`).
- cert-manager-not-installed graceful failure (`CertificateError=True, Reason=CertManagerCRDsMissing`).
- Allow-list rejection: out-of-list issuer → admission error.
- Mutual exclusion: `certificateRef` + manual secret → admission error.
- Cross-namespace `IssuerRef` → admission error.
- Cleanup on CR delete: `Certificate` and Secret both gone.
- Internal-cluster-auth Subject determinism across N members.

### Tooling

- E2E uses `kind` clusters with cert-manager pre-installed by the test harness; no real ACME calls except the LE-staging-on-externalDomain test which runs in a dedicated network-enabled variant.

## Metrics

PD §"Telemetry" lists the metric set. This TD commits intents and cardinality bounds; field names crystallize during implementation.

### Cardinality bounds (locked)

- **No per-issuer-name labels.** High cardinality + privacy concern (customer issuer names are sensitive).
- Permitted labels:
  - `resource_kind` ∈ {`MongoDB`, `MongoDBMultiCluster`, `MongoDBOpsManager`, `MongoDBSearch`}
  - `cert_type` ∈ {`server`, `agent_client`, `internal_cluster_auth`, `kmip_client`, `prometheus`}
  - `result` ∈ {`success`, `error`}
  - `member_cluster_index` (multi-cluster, bounded by member count)

### Metrics

| Metric | Type | Labels |
| --- | --- | --- |
| Certificates created | counter | resource_kind, cert_type, result |
| Certificates renewed | counter | resource_kind, cert_type, result |
| Certificate request failures | counter | resource_kind, cert_type, result |
| Time to issue | histogram | resource_kind, cert_type |
| Time to renew | histogram | resource_kind, cert_type |
| Multi-cluster Secret replication failures | counter | member_cluster_index |
| `mongod` restarts attributable to TLS rotation | counter | resource_kind |
| CA-bundle availability | gauge | resource_kind, status |
| cert-manager CRDs missing (cluster-level) | gauge | (none) |

### Telemetry gating

All cert-manager-related metrics are emitted only when `certManager.enabled=true`. Operators that haven't enabled cert-manager produce no new metrics.

## Known Limitations

1. Phase 1 supports per-deployment certificates only (one Certificate per cert-type per resource, SANs cover all members). Per-pod is described under Future Work.
2. KMIP client certificates with encrypted private keys are not supported in Phase 1. cert-manager does not produce password-protected keys natively.
3. JKS/PKCS12 truststore generation is not supported in Phase 1. Customers using JKS via `spec.opsManager.jvmParams` continue manual flow.
4. Public ACME CAs (Let's Encrypt, ZeroSSL, Google Trust, public Buypass) cannot issue `clientAuth` EKU after the dates published in [Let's Encrypt's 2025-05-14 announcement](https://letsencrypt.org/2025/05/14/ending-tls-client-authentication). Operator does not pre-validate; failures surface at runtime via `CertificateError`. Public Docs document the constraint.
5. ACME issuers (public **and** private — e.g. step-ca, smallstep) cannot validate cluster-internal DNS names. ACME's HTTP-01 and DNS-01 challenges require publicly-reachable hostnames or zones; certs whose SAN list contains only `*.svc.cluster.local`, `*.<resource>-svc.<ns>.svc.cluster.local`, or other cluster-internal names will fail at the cert-manager challenge stage (cf. Inventory §3.2). Customers using an ACME issuer **must** set `spec.connectivity.externalDomain` or populate `spec.connectivity.replicaSetHorizons` so the SAN list contains at least one externally-verifiable hostname. Operator does not pre-validate; failures surface at runtime via `CertificateError` with the verbatim cert-manager challenge error (typical text mentions "Failed to perform self check"/"http-01 challenge"). Public Docs document the constraint and remediation.
6. cert-manager not installed → CRs with `certificateRef` get `CertificateError=True, Reason=CertManagerCRDsMissing`; non-cert-manager CRs unaffected.
7. `ca.crt` empty on cert-manager Secret (SelfSigned, Vault PKI without chain) → `CACertificateAvailable=False`; user must set `spec.security.tls.ca` manually.
8. trust-manager not required, not integrated.
9. CA rotation logic remains manual (existing customer procedure unchanged).
10. Multi-cluster: cert-manager runs only in central cluster. Per-cluster mode is described under Future Work.
11. `CertificateError.message` is free-form, not part of the API contract; alerting consumers should not pattern-match on its text.

## Production Considerations

- **Helm install order**: cert-manager is a customer prerequisite; operator with `certManager.enabled=true` is installed afterwards.
- **CRD versioning**: no v1 → v2 bump. New optional fields only; backwards-compatible.
- **Upgrade path**: existing manual flows unchanged. Customers opt in by adding `certificateRef` per CR.
- **Migration**: per-CR mutual exclusion; user moves CR from manual to cert-manager flow by replacing manual fields with `certificateRef`. Operator handles cutover via existing PEM rotation path.
- **Namespace scoping**: `Issuer` must live in the same namespace as the CR. `ClusterIssuer` is cluster-scoped. Cross-namespace rejected at admission.
- **Secret cleanup**: operator deletes the cert-manager-issued Secret on CR delete (regardless of cert-manager's `--enable-certificate-owner-ref` flag).
- **RBAC delivery**: optional Helm template `operator-roles-certmanager.yaml` rendered when `certManager.enabled=true`. Mirrors existing `operator-roles-pvc-resize.yaml` pattern.
- **Allow-list semantics**: `Issuer` allow-list entry matches only same-namespace CRs. `ClusterIssuer` entries have no namespace constraint.
- **Documentation**: Public Docs cover (a) prerequisites + Issuer-config guidance, (b) per-issuer compatibility matrix (the Inventory table), (c) migration playbook from manual to cert-manager, (d) troubleshooting.

### Cross-team contacts

| Team | Collaboration |
| --- | --- |
| TSE / Support | Documentation review; troubleshooting guides for cert-manager integration; feedback on common TLS support tickets. |
| Field Engineering | Validation with customers using cert-manager today (StackIT and others); customer feedback on adoption pace. |
| Documentation Team | Public Docs setup guide, migration guide, troubleshooting, per-issuer compatibility matrix. |

## Implementation Plan

Phase 1 work is grouped into three sequenced main tasks. Each maps to a slice of the granular ticket list in the next section. Post-project items are sketched here for sequencing context; full mechanics are under Future Work.

### Phase 1 main tasks

#### Task 1 — Implement TLS server certificate automation

Foundation. Establishes the cert-manager integration end-to-end for server-cert sites: MongoDB / MongoDBMultiCluster, MongoDBOpsManager (OM server), AppDB server, MongoDBSearch, Prometheus.

Scope:

- Vendor `pkg/apis/certmanager/v1` + `pkg/apis/meta/v1` (Tickets — Vendor).
- `CertificateRef` + nested types and CRD embedding at the five server-cert sites (Tickets — types + CRD embedding).
- CEL validations + Helm values + optional RBAC template (Tickets — CEL, Helm, RBAC).
- New `controllers/operator/certs/certmanager/` package with the `Builder` for server-cert variants; SAN composition reuses existing helpers (Tickets — Builder; Algorithm(s) — SAN composition).
- Reconciler integration for server certs in `mongodbreplicaset` / `mongodbshardedcluster` / `mongodbmulticluster` / `mongodbstandalone` / `appdbreplicaset` / `mongodbopsmanager` / `mongodbsearch` (Tickets — reconciler integration).
- Watch wiring for `Certificate` + `Secret` with the label predicate (Tickets — watch wiring).
- Validating webhook extension for the server-cert-site rules (Tickets — webhook).
- `CertificateProvisioned` / `CertificateError` conditions (initial shape; CA-related condition lands in Task 3).
- CR-delete cleanup of `Certificate` + Secret (Tickets — cleanup).
- E2E for server-cert paths under both Evergreen variants (Tickets — E2E).

Exit criteria: a customer can enable `certManager.enabled`, point any server-cert site at a cert-manager Issuer, and get an automatically issued, mounted, and renewed certificate; manual flow remains untouched and mutually exclusive.

Dependencies: none (foundation).

#### Task 2 — Add client TLS certificate automation

Extends the Builder and validators to cover the three client-cert sites: agent client cert, internal-cluster-auth (dual-EKU), KMIP client.

Scope:

- Extend `Builder` for `clientAuth` and dual-EKU (`serverAuth + clientAuth`) variants.
- Internal-cluster-auth Subject computation hoisted into `cert_configurations.go` as `Options.ClusterAuthSubject` + round-trip determinism unit test (Tickets — Subject hoisting; Algorithm(s) — Internal-cluster-auth Subject).
- AppDB X.509 dual-EKU special case (when `Authentication.InternalCluster == X509`) — same single cert.
- Mutual-exclusion CEL for the three client-cert sites (KMIP uses `clientCertificatePrefix`, others use secret-ref shapes).
- Reconciler integration for client-cert paths in the same controllers, reusing the Task 1 watch + Secret pipeline.
- Validating webhook extension for the client-cert-site rules.
- E2E for client-cert paths (agent X.509 auth, internal-cluster-auth across N members, KMIP backup-daemon).

Exit criteria: every Phase-1 cert type is automatable end-to-end via a single `certificateRef` per site.

Dependencies: Task 1 (Builder framework, watch wiring, status conditions). Can begin in parallel after Task 1 vendoring + Builder skeleton land; full integration after Task 1 server-cert path is green.

#### Task 3 — Add CA certificate automation (best-effort distribution)

Best-effort copy of `ca.crt` from cert-manager-issued Secrets into operator-owned ConfigMaps for MongoDB-CA, OM-CA, AppDB-CA. No CA issuance, no CA private-key handling, no CA rotation logic.

Scope:

- `assembleCABundle` helper + table tests (Tickets — assembleCABundle; Algorithm(s) — CA-bundle assembly).
- Three call sites (MongoDB, OM, AppDB) with key aliasing (`ca-pem` / `mms-ca.crt`); auto-fill of `spec.security.tls.ca` and `spec.applicationDatabase.security.tls.ca` only when unset (Design — CA-bundle distribution).
- `CACertificateAvailable` status condition with reasons (`Provisioned`, `NoCAEmittedFromIssuer`, `UserManaged`, `Unknown`).
- Lazy ConfigMap cleanup on user-driven `tls.ca` override or `certificateRef` removal.
- Forward-compat (a): tighten "update on content change" semantics on existing CA replicators (`mongodbopsmanager_controller.go:1347`, `mongodbmultireplicaset_controller.go:451`) + tests.
- Forward-compat (b): emit `mongodb.com/managed-mode=central` label on every operator-created `Certificate`.
- E2E for CA-bundle auto-fill, SelfSigned skip, user-override preservation.

Exit criteria: a customer using a CA / Vault / Venafi / External issuer no longer needs to maintain `spec.security.tls.ca` themselves on the happy path; SelfSigned and chain-less issuers degrade gracefully via status condition.

Dependencies: Task 1 (cert-manager-issued Secret to read `ca.crt` from). Can run in parallel with Task 2 after Task 1 server-cert path lands.

### Post-project tasks

Detailed mechanics under Future Work; listed here for sequencing.

#### Task 4 — Per-cluster issuers support

Operator-level Helm value flips multi-cluster mode from "cert-manager in central + Secret replication" to "cert-manager in every member cluster + local issuance + multi-root CA bundle." See Future Work — Per-cluster cert-manager mode. Effort ~9 EW. Phase-1 forward-compat items make this purely additive.

#### Task 5 — Agent `rotateCertificates` support

When the agent ships `rotateCertificates`, operator calls it from the PEM update path → eliminates sequential `mongod` restarts on renewal. See Future Work — Agent `rotateCertificates` integration. EA agent roadmap dependency.

#### Task 6 — Per-pod certificates

Move from per-deployment "uber" Secret to per-process Certificate + per-pod mount. Multiple sub-options (uber-PEM with per-ordinal entries, sidecar from bundle, custom mutating webhook); recommended path is uber-PEM combined with Task 5's `rotateCertificates`. See Future Work — Per-pod / per-process certificates.

## Tickets

Suggested ticket breakdown for the implementation epic (CLOUDP-389489).

1. Vendor cert-manager `pkg/apis/certmanager/v1` + `pkg/apis/meta/v1`.
2. `CertificateRef` Go types + `X509Subject` + `CertificatePrivateKey` + `IssuerRef`.
3. CRD embedding at all 8 sites + CRD generation (config/crd, helm_chart/crds, public/crds.yaml).
4. CEL validations: `Kind` enum + per-site mutual exclusion + Subject-on-internal-cluster rejection.
5. Helm values `certManager.enabled` + `certManager.allowedIssuers`.
6. Optional RBAC template `operator-roles-certmanager.yaml` (mirroring PVC-resize).
7. New package `controllers/operator/certs/certmanager/`: Builder + per-cert-type Subject/usages/SAN.
8. Internal-cluster-auth Subject computation hoisted into `cert_configurations.go` (`Options.ClusterAuthSubject`) + round-trip determinism unit test.
9. `assembleCABundle` helper + table tests.
10. Reconciler integration: MongoDB + MongoDBMultiCluster + MongoDBOpsManager (OM + AppDB) + MongoDBSearch.
11. Watch wiring: cert-manager Certificate + Secret with label predicate.
12. Validating webhook extension: allow-list + cross-namespace + per-site mutual exclusion.
13. Status conditions: `CertificateProvisioned` + `CertificateError` + `CACertificateAvailable` per CR type.
14. CR delete cleanup: Certificate + Secret + operator-owned ConfigMap. Includes lazy cleanup of the operator-owned ConfigMap on user-driven `tls.ca` override or `certificateRef` removal (Design — CA-bundle distribution — Lazy cleanup).
15. CA-bundle distribution call sites: MongoDB + OM + AppDB.
16. Multi-cluster CA replicator update semantics tightened (forward-compat (a) under Design — Phase-2 forward-compatibility).
17. `mongodb.com/managed-mode` label on every operator-created Certificate (forward-compat (b) under Design — Phase-2 forward-compatibility).
18. Telemetry: lifecycle counters, time histograms, replication failures, restart counters, CA-bundle gauge, CRD-missing gauge.
19. E2E variant matrix: cert-manager v1.13.0 + latest stable.
20. Documentation: user guide, migration guide, troubleshooting, per-issuer compatibility matrix in Public Docs.

## Open Questions

1. cert-manager v1.13.0 is the proposed floor — confirm at GA whether any field-customer cluster pins older.
2. Internal-cluster-auth Subject template (O/OU/CN format) — finalize before code lands; ensure determinism test fixtures match.
3. Allow-list semantics around `ClusterIssuer` + namespace — security review before GA.
4. Migration playbook: detailed step-by-step for moving from manual to cert-manager flow per CR — owned by docs team.
5. Telemetry field names — defer to implementation; cardinality bounds locked here.
6. KMIP encrypted-key support — defer to demand signal post-GA.
7. Subject field on `CertificateRef` for non-internal-cluster sites — keep as PD baseline; revisit if no field-customer ever sets it within first 6 months post-GA.
8. Public Docs URL slug for cert-manager compatibility page — TBD; consumed as a constant in operator error-message construction (Design — Status reporting). Documentation ticket (Tickets — Documentation) owns finalization before GA.

## Future Work (out of scope for Phase 1)

### Per-cluster cert-manager mode

Operator-level Helm value `multiCluster.certManager.mode: central|per-cluster` (CRD untouched — this is operator-config, not CRD-API). In `per-cluster` mode:

- cert-manager installed in every member cluster (customer responsibility); central cluster needs cert-manager only when AppDB/OM live there.
- Customer pre-creates the Issuer/ClusterIssuer with the same name in every member cluster.
- Each cert (server, agent client, internal-cluster-auth) is issued in the cluster where it is consumed — server certs in members, agent client + internal-cluster-auth also in members (one Certificate per cluster, deterministic Subject identical across clusters; mongod's cluster-auth check is Subject-equality, not byte-equality). This relies on the Phase-1 internal-cluster-auth Subject template being locked deterministically (Open Questions item 2); future per-cluster work cannot start before the template is finalized in Phase 1.
- KMIP client cert stays central-issued (consumed by OM backup daemon; multi-cluster KMIP not supported today — TODO `controllers/operator/mongodbopsmanager_controller.go:1036`).
- No cross-cluster Secret replication for cert-manager-issued certs.

CA bundle in per-cluster mode: operator assembles a multi-root union from each cluster's `ca.crt`. The same `assembleCABundle` helper handles it (Phase 1's single-blob input → future per-cluster N-blob input). Auto-fill rule unchanged.

Switching central ↔ per-cluster after GA is safe without a migration tool, given:

- `mongodb.com/managed-mode` label on every operator-created Certificate from Phase 1 (forward-compat (b) under Design — Phase-2 forward-compatibility).
- Cleanup of opposite-mode Certificates is gated on "all new-mode Certificates Ready" (Ready-before-cleanup guardrail).

Effort estimate: ~9 EW (refined from initial 6 EW after symmetric-issuance correction). Drivers: per-cluster `Certificate` Create call sites (3 client-cert types + N member certs), Issuer pre-flight per cluster, multi-root CA union, status per cluster, mode-switch cleanup helper, dual-mode E2E coverage.

### Per-pod / per-process certificates

SPIKE §"Splitting certificates" — four sub-options (spoditor-style external webhook, custom mutating webhook, uber-PEM with per-ordinal entries, sidecar from bundle Secret). Recommended: combined uber-PEM + agent `rotateCertificates` once available.

### cert-manager CSI driver

SPIKE Option 2. Mounts certs directly into pods; no intermediate Secret. Dependencies: `SERVER-109921` (separate key/cert paths in mongod), agent `rotateCertificates`. Phase 1 design must not preclude.

### Agent `rotateCertificates` integration

Once the agent supports `rotateCertificates`, the operator can call from the PEM update path; eliminates sequential `mongod` restarts on renewal. EA agent roadmap.

### KMIP encrypted-key support

cert-manager doesn't natively produce password-protected keys. Either custom plumbing or KMS supporting unencrypted client keys. Track customer demand post-GA.

### JKS/PKCS12 truststore generation

OM and AppDB consume CA as raw PEM today; JKS not needed. Track customer demand. trust-manager could replace operator-side ConfigMap copy logic if future work needs Java keystore output.

### trust-manager integration

Not required Phase 1. Could replace operator-side ConfigMap assembly logic if needs evolve.

## References

- PD+Scope: [doc](https://docs.google.com/document/d/1AQ7QqH0ZAtL4OzxD7VDU6YhSlZ3vY57bcAPczWbbP-k/edit)
- Pre-TD investigation — Certificate Inventory and Per-Issuer Feasibility: [doc tab](https://docs.google.com/document/d/1nB722OaeCzPomlKmlw5h1PqLbI1Lg8pWkHLDugD4iPs/edit?tab=t.0#heading=h.bbh8k1ywuwsz)
- Pre-TD investigation — CertificateRef field shape: [doc tab](https://docs.google.com/document/d/1nB722OaeCzPomlKmlw5h1PqLbI1Lg8pWkHLDugD4iPs/edit?tab=t.0#heading=h.adtf3f8jo3h5)
- Prior SPIKE: [doc](https://docs.google.com/document/d/1-eA4ZZRnqLkvTsgo6X4WoEt2_fIZqHBRHz4gLdyb2_0/edit)
- cert-manager API docs: <https://cert-manager.io/docs/reference/api-docs/>
- cert-manager importing guidance: <https://cert-manager.io/docs/contributing/importing/>
- cert-manager CSI driver: <https://cert-manager.io/docs/usage/csi-driver/>
- trust-manager: <https://cert-manager.io/docs/trust/trust-manager/>
- Let's Encrypt 2025-05-14 announcement: <https://letsencrypt.org/2025/05/14/ending-tls-client-authentication>
- Knative cert-manager allow-list pattern: <https://knative.dev/docs/serving/encryption/configure-certmanager-integration/#configuring-issuers>
- `SERVER-109921` (mongod separate key/cert paths)
- Atlas online certificate rotation (rotateCertificates command)
