# MongoDBSearch Proxy API Design Alternatives

## Open Design Problems

### External exposure for Managed LB

**Problem:** The operator creates a ClusterIP Service as the entrypoint for the managed proxy. Its address (`<name>.svc.cluster.local`) is only reachable inside the cluster. External MongoDB deployments (e.g. OCP, on-prem mongod outside Kubernetes) cannot reach it.

This breaks in two ways when the user exposes the service via an OCP Route, LoadBalancer Service, or Ingress:

1. **SNI mismatch**: Envoy's filter chains match SNI against the cluster-internal proxy service FQDN. When mongod connects via an external hostname (e.g. an OCP route), the SNI in the TLS ClientHello is that external hostname — Envoy drops the connection.
2. **Certificate SAN mismatch**: The TLS certificate presented by Envoy must have the external hostname as a SAN.

> Example: *"The spec.lb.endpoint will be set to the OCP route hostnames and because of that the SNI would be those route hostnames. And as a result of this the envoy config must have the route hostnames as server names and not the proxy services. Similarly the certificate would also need to have the route hostnames as SAN."*

**Prior art — MongoDB ReplicaSet external access:**

The operator already handles this for MongoDB via `spec.externalAccess`:
- Creates one LoadBalancer Service per pod (type overridable via `spec.externalAccess.externalService.spec`)
- `spec.externalAccess.externalDomain` sets the externally reachable hostname, which the operator uses in MongoDB's process configuration
- Cloud-provider annotations supported via `spec.externalAccess.externalService.annotations`
- The operator does NOT create Ingress or OCP Routes — only Services

For Search managed LB, there is one proxy Service per shard (not per pod), so external exposure is simpler: one Service to expose per shard. The same pattern applies.

**The chicken-and-egg problem:**

If the operator creates a `LoadBalancer` Service, the external hostname is assigned by the cloud provider after creation (in `status.loadBalancer.ingress`). The operator can't know it upfront to configure Envoy SNI.

Two approaches:
- **User-declared endpoint (recommended):** User provides the external endpoint explicitly in the spec. Operator configures Envoy SNI immediately. User is responsible for making the endpoint resolve (DNS, OCP Route, etc.). This mirrors `externalAccess.externalDomain` on the MongoDB CR.
- **Operator-discovered endpoint (complex):** Operator creates the Service, reads `status.loadBalancer.ingress` after the cloud provider assigns it, re-reconciles to update Envoy config. Requires watching Service status and tolerating a multi-reconcile bootstrap.

The user-declared approach is the right starting point — it avoids the bootstrap problem and matches the existing operator pattern.

**Open questions:**
1. Should `externalEndpoint` be required when `externalAccess` is set, or can the operator derive it from the Service status (opt-in to the complex path)?
2. For sharded clusters, the endpoint template must use `{shardName}` — same convention as Unmanaged mode. Should this be validated via CEL?
3. Does the operator need to reconfigure `mongotHost` when `externalEndpoint` is set, or is that always the user's responsibility for external MongoDB?

---

## Current Design

> Note: `image` was removed from `EnvoyConfig` in PR #912 and may not be visible on this branch yet.

```yaml
spec:
  lb:
    mode: Managed
    envoy:
      resourceRequirements:
        requests: { cpu: 100m, memory: 128Mi }
        limits:   { cpu: 500m, memory: 512Mi }
```

```yaml
spec:
  lb:
    mode: Unmanaged
    endpoint: "lb-{shardName}.example.com:27028"
```

**Issues:**
- `lb` is abbreviated (Kubernetes convention uses full words)
- `mode` is a stringly-typed discriminant; `endpoint` and `envoy` are exclusive but live at the same level
- `envoy` leaks implementation detail — the field name will outlive any proxy swap
- No external access support

---

## Option A — Discriminated Union (recommended)

Drop `mode`. The presence of a sub-object is the discriminant.

**Minimal examples:**

```yaml
# Managed — all defaults
spec:
  loadBalancer:
    managed: {}
```

```yaml
# Managed — with resource override
spec:
  loadBalancer:
    managed:
      resourceRequirements:
        requests: { cpu: 200m, memory: 256Mi }
```

```yaml
# Managed — with external access (ReplicaSet)
spec:
  loadBalancer:
    managed:
      externalAccess:
        externalEndpoint: "search.apps.mycluster.example.com:27028"
        externalService:
          spec:
            type: LoadBalancer
          annotations:
            service.beta.kubernetes.io/aws-load-balancer-type: nlb
```

```yaml
# Managed — with external access (sharded)
spec:
  loadBalancer:
    managed:
      externalAccess:
        externalEndpoint: "search-{shardName}.apps.mycluster.example.com:27028"
```

```yaml
# Managed — with deployment override
spec:
  loadBalancer:
    managed:
      deployment:
        spec:
          template:
            spec:
              nodeSelector:
                kubernetes.io/os: linux
```

```yaml
# Unmanaged — ReplicaSet
spec:
  loadBalancer:
    unmanaged:
      endpoint: "lb.example.com:27028"
```

```yaml
# Unmanaged — sharded
spec:
  loadBalancer:
    unmanaged:
      endpoint: "lb-{shardName}.example.com:27028"
```

**Go types:**

```go
// LoadBalancerConfig configures how mongod/mongos connect to mongot.
// Exactly one of Managed or Unmanaged must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.managed) || has(self.unmanaged)) && !(has(self.managed) && has(self.unmanaged))",message="exactly one of managed or unmanaged must be set"
type LoadBalancerConfig struct {
    // Managed enables the operator-managed load balancer. All fields are optional; defaults apply if omitted.
    // +optional
    Managed *ManagedLBConfig `json:"managed,omitempty"`
    // Unmanaged configures a user-provided L7 load balancer.
    // +optional
    Unmanaged *UnmanagedLBConfig `json:"unmanaged,omitempty"`
}

type ManagedLBConfig struct {
    // ExternalAccess configures how the managed load balancer Service is exposed outside the cluster.
    // When set, the Service type is overridden and Envoy uses ExternalEndpoint for SNI matching.
    // +optional
    ExternalAccess *LBExternalAccessConfig `json:"externalAccess,omitempty"`
    // ResourceRequirements for the load balancer container.
    // Defaults to requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: 500m, memory: 512Mi}.
    // +optional
    ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
    // Deployment is merged into the operator-created Deployment at the end of the reconcile loop.
    // Analogous to spec.statefulSet on the MongoDBSearch resource.
    // +optional
    Deployment *common.DeploymentConfiguration `json:"deployment,omitempty"`
}

type UnmanagedLBConfig struct {
    // Endpoint is the BYO load balancer address for ReplicaSet sources,
    // or a template for sharded clusters. Sharded templates must contain
    // at least one {shardName} placeholder.
    // Example: "lb-{shardName}.example.com:27028"
    // +kubebuilder:validation:Required
    Endpoint string `json:"endpoint"`
}

type ProxyExternalAccessConfig struct {
    // ExternalEndpoint is the externally reachable address for mongod to connect to the managed proxy.
    // For sharded clusters, must contain a {shardName} placeholder.
    // Used as the Envoy SNI server name — the TLS certificate must have this hostname as a SAN.
    // +kubebuilder:validation:Required
    ExternalEndpoint string `json:"externalEndpoint"`
    // ExternalService allows overriding the proxy Service spec (type, annotations).
    // Defaults to type: LoadBalancer if not set.
    // +optional
    ExternalService *ExternalServiceConfiguration `json:"externalService,omitempty"`
}
```

> **Note:** `common.DeploymentConfiguration` does not exist yet and would need to be created,
> analogous to `common.StatefulSetConfiguration` (wrapping `appsv1.DeploymentSpec` + metadata).

**Pros:**
- Self-documenting: valid configurations are structurally apparent
- No Envoy in the public API
- `managed: {}` is valid — natural progressive disclosure
- Exclusive fields are scoped to their mode's sub-object
- External access follows the same pattern as `spec.externalAccess` on MongoDB CRs

**Cons:**
- CEL rule required for mutual exclusivity
- Internal method renames (`IsLBModeManaged` → `IsManagedProxy`, etc.)
- `common.DeploymentConfiguration` needs to be introduced

---

## Option B — Flat with Implicit Mode

No `mode` field. Field presence determines behavior. Setting `endpoint` enables Unmanaged; omitting it enables Managed.

**Minimal examples:**

```yaml
# Managed — all defaults
spec:
  loadBalancer: {}
```

```yaml
# Managed — with resource override
spec:
  loadBalancer:
    resourceRequirements:
      requests: { cpu: 200m, memory: 256Mi }
```

```yaml
# Managed — with external access
spec:
  loadBalancer:
    externalAccess:
      externalEndpoint: "search.apps.mycluster.example.com:27028"
      externalService:
        spec:
          type: LoadBalancer
```

```yaml
# Unmanaged — ReplicaSet
spec:
  loadBalancer:
    endpoint: "lb.example.com:27028"
```

```yaml
# Unmanaged — sharded
spec:
  loadBalancer:
    endpoint: "lb-{shardName}.example.com:27028"
```

**Go types:**

```go
// LoadBalancerConfig configures how mongod/mongos connect to mongot.
// Setting endpoint enables Unmanaged mode (BYO L7 LB).
// Omitting endpoint enables Managed mode (operator-deployed load balancer).
// +kubebuilder:validation:XValidation:rule="!(has(self.endpoint) && (has(self.resourceRequirements) || has(self.deployment) || has(self.externalAccess)))",message="endpoint (unmanaged) and managed-only fields are mutually exclusive"
type LoadBalancerConfig struct {
    // Endpoint is the BYO load balancer address. Setting this enables Unmanaged mode.
    // For sharded clusters, must contain at least one {shardName} placeholder.
    // +optional
    Endpoint string `json:"endpoint,omitempty"`
    // ExternalAccess configures how the managed load balancer Service is exposed outside the cluster.
    // Only applies in Managed mode.
    // +optional
    ExternalAccess *LBExternalAccessConfig `json:"externalAccess,omitempty"`
    // ResourceRequirements for the managed load balancer container. Only applies in Managed mode.
    // +optional
    ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
    // Deployment is merged into the operator-created Deployment. Only applies in Managed mode.
    // +optional
    Deployment *common.DeploymentConfiguration `json:"deployment,omitempty"`
}
```

**Pros:** Flat, minimal nesting, fewest fields. `loadBalancer` is always visible.

**Cons:**
- Implicit modes are easy to misread — `endpoint` presence is not obvious as a mode switch
- Managed-only fields (`externalAccess`, `resourceRequirements`, `deployment`) are meaningless in Unmanaged mode but not visually scoped
- Validation enforced entirely by CEL, not structure

---

## Option C — Keep `mode`, Minimal Rename

Smallest change: rename `lb` → `loadBalancer`, drop `envoy`, add `externalAccess` under settings.

**Minimal examples:**

```yaml
# Managed — all defaults
spec:
  loadBalancer:
    mode: Managed
```

```yaml
# Managed — with external access
spec:
  loadBalancer:
    mode: Managed
    settings:
      externalAccess:
        externalEndpoint: "search.apps.mycluster.example.com:27028"
        externalService:
          spec:
            type: LoadBalancer
```

```yaml
# Managed — with deployment override
spec:
  loadBalancer:
    mode: Managed
    settings:
      deployment:
        spec:
          template:
            spec:
              nodeSelector:
                kubernetes.io/os: linux
```

```yaml
# Unmanaged — ReplicaSet
spec:
  loadBalancer:
    mode: Unmanaged
    endpoint: "lb.example.com:27028"
```

```yaml
# Unmanaged — sharded
spec:
  loadBalancer:
    mode: Unmanaged
    endpoint: "lb-{shardName}.example.com:27028"
```

**Go types:**

```go
type LoadBalancerConfig struct {
    // +kubebuilder:validation:Required
    Mode LBMode `json:"mode"`
    // Endpoint is the BYO LB address. Only applies when Mode is Unmanaged.
    // +optional
    Endpoint string `json:"endpoint,omitempty"`
    // Settings configures the operator-managed proxy. Only applies when Mode is Managed.
    // +optional
    Settings *ManagedProxySettings `json:"settings,omitempty"`
}

type ManagedProxySettings struct {
    // ExternalAccess configures how the managed proxy Service is exposed outside the cluster.
    // +optional
    ExternalAccess *ProxyExternalAccessConfig `json:"externalAccess,omitempty"`
    // ResourceRequirements for the managed proxy container.
    // +optional
    ResourceRequirements *corev1.ResourceRequirements `json:"resourceRequirements,omitempty"`
    // Deployment is merged into the operator-created Deployment at the end of the reconcile loop.
    // Analogous to spec.statefulSet on the MongoDBSearch resource.
    // +optional
    Deployment *common.DeploymentConfiguration `json:"deployment,omitempty"`
}
```

**Pros:** No structural change, easy migration, removes Envoy from API.

**Cons:** Still has the mode-as-discriminant problem; `settings` is generic; `endpoint` and `settings` remain exclusive but visually co-equal.

---

## Shared type: `LBExternalAccessConfig`

Used identically across all options:

```go
type LBExternalAccessConfig struct {
    // ExternalEndpoint is the externally reachable address for mongod to connect to the managed load balancer.
    // For sharded clusters, must contain a {shardName} placeholder.
    // Used as the Envoy SNI server name — the TLS certificate must have this hostname as a SAN.
    // +kubebuilder:validation:Required
    ExternalEndpoint string `json:"externalEndpoint"`
    // ExternalService allows overriding the load balancer Service spec (type, annotations).
    // Defaults to type: LoadBalancer if not set.
    // +optional
    ExternalService *ExternalServiceConfiguration `json:"externalService,omitempty"`
}
```

---

## New type needed: `common.DeploymentConfiguration`

All options include a `deployment` escape hatch. No `DeploymentConfiguration` wrapper exists yet in `mongodb-community-operator/api/v1/common`. A new type would be needed, mirroring `StatefulSetConfiguration`:

```go
type DeploymentConfiguration struct {
    // +kubebuilder:pruning:PreserveUnknownFields
    SpecWrapper DeploymentSpecWrapper `json:"spec"`
    // +optional
    MetadataWrapper DeploymentMetadataWrapper `json:"metadata"`
}
```

---

## Summary

| | Field name | Mode signal | Implementation detail | Structural safety | New types needed |
|---|---|---|---|---|---|
| **Current** | `lb` (abbreviated) | `mode` string | `envoy` exposed | weak | — |
| **A (recommended)** | `loadBalancer` | sub-object presence | hidden | strong | `DeploymentConfiguration` |
| **B (flat)** | `loadBalancer` | field presence (implicit) | hidden | weak | `DeploymentConfiguration` |
| **C (minimal)** | `loadBalancer` | `mode` string | hidden | weak | `DeploymentConfiguration` |
