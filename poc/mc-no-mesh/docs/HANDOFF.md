# No-mesh MongoDB multi-cluster connectivity handoff

Last updated: **2026-09-08**

This document consolidates the investigation, code analysis, implementation,
and live OpenShift proof-of-concept work performed in this session. It includes
the delegated investigations in:

- `mongodb/mongodb-kubernetes`
- `~/mdb/mongo`
- `~/mdb/mms-automation`
- the two OpenShift clusters used for the PoCs

The detailed per-PoC inventories remain in:

- [`REPORT.md`](REPORT.md) - internal pod-Service FQDN PoC in `lsierant`
- [`LSIERANT2-EXTERNAL-DNS.md`](LSIERANT2-EXTERNAL-DNS.md) - external process
  names and namespace DNS PoC in `lsierant2`
- [`MDBMC-STATUS-LINK.md`](MDBMC-STATUS-LINK.md) - missing project URL
- [`CLOUD-QA-ORG-PROVENANCE.md`](CLOUD-QA-ORG-PROVENANCE.md) - Cloud QA
  organization provenance

## 1. Executive conclusion

**Validated:** two OpenShift PoCs proved namespace-scoped, bidirectional
member transport through duplicate Services, Envoy, and TLS-passthrough
Routes. End-to-end TLS, authentication, and `w:3` replication worked in a 1+2
replica set without a service mesh or per-member LoadBalancers.

**Target but not yet validated:** a separate `client` replica-set horizon on
routable `host:443` endpoints is the recommended way to provide a normal
driver topology inside and outside the clusters. No client horizon, public
DNS, or external driver failover test was deployed.

The resulting product direction is a two-plane design:

1. **Member transport plane**
   - mongod replication, heartbeats, initial sync, and Automation Agents use
     stable per-member process names on port `27017`;
   - every cluster contains a local Service for every member;
   - the owning Service selects the mongod pod;
   - a remote duplicate Service selects a namespace Envoy relay;
   - Envoy sends the unchanged MongoDB TLS stream to the destination cluster's
     OpenShift router or equivalent gateway on port `443`;
   - the destination passthrough Route uses SNI to select the owning member
     Service and forwards to mongod on `27017`.

2. **Client topology plane**
   - clients use a separate replica-set horizon, for example `client`;
   - every member advertises a distinct externally routable hostname on port
     `443`;
   - public or enterprise DNS resolves each hostname to its owning cluster's
     router;
   - one passthrough Route per member sends `443` to the local mongod Service
     on `27017`;
   - the original TLS SNI reaches mongod, which selects the `client` horizon
     and returns all members with port `443`.

This avoids:

- a service mesh;
- one LoadBalancer per mongod;
- cluster-wide DNS changes;
- topology-driven MongoDB pod restarts;
- mongod changes for the initial implementation.

It does **not** eliminate all infrastructure integration. A production client
horizon still needs routable DNS names, reachable ingress/router addresses,
and a passthrough routing API. Gateway API also requires its CRDs and
controller to be installed by the platform.

## 2. Original requirements

The investigation optimized for:

- bidirectional connectivity between all mongod members across clusters;
- Automation Agent connectivity to every configured mongod process;
- no Istio or other mandatory service mesh;
- no per-member LoadBalancer;
- no cluster-admin dependency after initial operator/platform setup;
- resources deployable in one namespace in each cluster where possible;
- ability to use an existing OpenShift router or Gateway API listener on
  external port `443`;
- no pod rollout when replica-set membership changes;
- end-to-end MongoDB TLS without proxy termination;
- a normal replica-set client experience from inside and outside the clusters.

## 3. Core protocol findings

### 3.1 DNS, TLS identity, and SNI are compatible

MongoDB connects using a logical hostname. DNS supplies the socket IP but does
not replace that logical hostname. Unless the target is an IP literal, the
MongoDB TLS ClientHello uses the original hostname as SNI, and certificate
verification uses that same hostname.

This makes the following path valid:

```text
logical member name:27017
  -> namespace DNS returns a local Service IP
  -> Service selects Envoy
  -> Envoy copies the original TLS stream to remote-router:443
  -> router selects a passthrough Route using original SNI
  -> Route targets owning Service:27017
  -> mongod terminates TLS
```

Envoy must not terminate TLS and must not configure an upstream TLS transport
socket in this path. The original MongoDB ClientHello has to reach the
destination router and mongod unchanged.

### 3.2 Port translation is valid

Neither SNI nor a certificate contains the TCP destination port. A router can
accept a connection on `443` and forward it to a Service target on `27017`.
Similarly, a replica-set horizon can advertise `host:443` while mongod itself
continues to listen on `27017`.

The limitation is that SNI contains only the hostname. The same hostname
cannot distinguish normal mongod traffic from an ephemeral mongod on
`port+1`. Separate SNI hostnames or separate external ports would be needed
for two backends.

### 3.3 Passthrough is mandatory

The ingress must preserve the original TLS session:

- OpenShift Route: `spec.tls.termination: passthrough`
- Gateway API: a `TLS` listener in passthrough mode plus `TLSRoute`

TLS termination at Envoy, the router, or the Gateway would prevent mongod from
seeing the original client SNI and would break horizon selection unless a new
MongoDB-aware TLS session were deliberately originated.

## 4. Options investigated

| Option | Result | Why |
|---|---|---|
| Multi-cluster service mesh | Technically best transparency, not selected as the baseline | Requires platform installation and cluster-wide administration |
| One LoadBalancer per mongod | Works, already understood | Operational and cost problem grows with member count |
| One namespace L4 LoadBalancer plus Envoy SNI routing | Viable | Operator can own it, but it still creates a dedicated external endpoint per cluster |
| Existing OpenShift passthrough Routes | **Validated and used** | Namespace resources, shared router port `443`, SNI dispatch, arbitrary backend port |
| Gateway API `TLSRoute` | Viable when installed | More portable API, but CRD/controller/Gateway provisioning is a platform responsibility |
| Namespace Envoy relay | **Validated and used** | Provides local `27017` to remote router `443` translation while preserving TLS |
| Namespace split-horizon DNS | **Validated and used in `lsierant2`** | Redirects stable external identities to local Services without changing cluster DNS |
| Cluster CoreDNS/OpenShift DNS customization | Works globally | Requires platform administration and affects all workloads using the zone |
| `hostAliases` | Rejected | PodSpec changes require StatefulSet pod replacement for topology updates |
| ConfigMap-mounted `/etc/hosts` | Rejected | kubelet owns `/etc/hosts`; `subPath` does not hot-update; sidecar rewriting is unsupported |
| Selectorless Services/managed EndpointSlices | Possible building block | Requires endpoint lifecycle management and does not itself provide multi-host SNI dispatch |
| DNS without a local relay | Insufficient | DNS can change an IP but cannot translate mongod's destination port `27017` to router port `443` |
| TLS-in-TLS tunnel or HTTP CONNECT | Possible but more complex | Requires an explicit tunnel protocol/client integration and adds another TLS/control layer |
| Skupper or similar application interconnect | Technically capable | Adds another multi-cluster platform and lifecycle rather than a small operator-owned namespace component |
| Mongod node-local endpoint override | Technically plausible, high risk | Requires server changes across connection pools, self-identification, failure handling, and mixed-version rollout |
| Replica-set horizons for replication routing | Rejected | Horizons change inbound client topology views, not outbound member dialing |
| Replica-set horizons for driver access | **Recommended** | Cleanly advertises routable per-member `host:443` endpoints to normal drivers |

## 5. OpenShift Route and Gateway API investigation

### 5.1 `.cluster.local` Route experiment

A delegated OpenShift experiment used:

```text
default/api-kubehosted-9h9y-p1-openshiftapps-com:6443/admin-kubehosted
```

It created:

- namespace `lsierant`;
- a TLS echo Deployment;
- a Service on TCP `8443`;
- a passthrough Route with host
  `tls-echo.lsierant.svc.cluster.local`.

OpenShift admitted the Route even though the hostname was not publicly
resolvable. The backend was then changed from HTTPS behavior to arbitrary raw
TLS-wrapped TCP echo, which also succeeded through the router:

```bash
printf 'raw-tcp-test\n' | openssl s_client -quiet \
  -connect router-default.apps.kubehosted.9h9y.p1.openshiftapps.com:443 \
  -servername tls-echo.lsierant.svc.cluster.local
```

The result established:

- stock OpenShift Route validation checks DNS syntax, not public
  resolvability;
- passthrough Route is a TCP proxy after SNI-based backend selection;
- external `443` can target an arbitrary backend TCP port such as `27017`;
- the router does not need a certificate for a passthrough Route.

This is not a universal guarantee. Router domain allow/deny policy, custom
admission, or organizational hostname policy can still reject internal names.
A production operator should perform a preflight admission/capability check.

### 5.2 Gateway API relationship

OpenShift Route and Gateway API are independent APIs and controllers. An
OpenShift Route is not automatically translated to `TLSRoute`.

The conceptual Gateway API equivalent is:

- `Gateway` listener:
  - port `443`;
  - protocol `TLS`;
  - passthrough mode;
- `TLSRoute.hostnames`:
  - the member SNI hostnames;
- `TLSRoute.backendRefs`:
  - the local owning member Service and its MongoDB port.

Attachment can fail even when the hostname is valid:

- the listener hostname and `TLSRoute` hostname must intersect;
- `allowedRoutes` controls namespaces allowed to attach;
- a `ReferenceGrant` may be needed for cross-namespace backend references;
- the implementation must support `TLSRoute`.

The inspected OpenShift 4.22.7 cluster had `Gateway`, `GatewayClass`, and
`HTTPRoute` base CRDs, but:

- no `TLSRoute` CRD;
- no `GatewayClass` instances;
- no usable Gateway controller.

Gateway API was therefore not usable without cluster-level enablement or a
controller/dataplane installation. OpenShift Route was the practical PoC API.

## 6. Namespace Envoy relay design

Each cluster has local representations of every member:

- an owning Service selects its mongod pod;
- a remote duplicate of another cluster's Service selects local Envoy.

The relay listener accepts local traffic on `27017` and uses
`envoy.filters.network.tcp_proxy` to connect to the destination router on
`443`. The destination router performs member dispatch using the unchanged
MongoDB SNI.

The two-cluster PoCs use one opposite-router upstream per Envoy configuration.
That is enough when there is exactly one remote cluster.

For three or more clusters, production design must choose one of:

1. one shared Envoy that uses `tls_inspector` and SNI-based filter chains to
   choose the correct destination router;
2. one relay pool per destination cluster plus target-specific Service
   selectors;
3. a dynamically managed xDS routing table.

The current global `remoteDuplicatePodService` selector and single-upstream
Envoy configuration do not by themselves generalize to multiple possible
remote destination clusters. This was not tested by the 1+2, two-cluster
PoCs.

The PoCs use mutable image
`docker.io/envoyproxy/envoy:v1.37-latest`. Production manifests and a
reproducible test record must pin an image digest.

Production hardening still needs:

- at least two relay replicas;
- topology spread or anti-affinity;
- PodDisruptionBudget;
- resource sizing and backpressure limits;
- connection and error metrics;
- NetworkPolicies;
- dynamic or safely rolled configuration updates;
- failure testing during router, relay, and cluster outages.

## 7. DNS investigation

### 7.1 Cluster DNS versus namespace DNS

Changing the platform CoreDNS/OpenShift DNS configuration would transparently
affect every workload using the configured zone. It is the broadest solution,
but it requires cluster administration and creates a platform-level ownership
boundary.

A namespace DNS Deployment is non-invasive to other workloads, but Kubernetes
does not automatically use it. Each opted-in pod must set:

```yaml
dnsPolicy: None
dnsConfig:
  nameservers:
    - <numeric ClusterIP of namespace DNS Service>
  searches:
    - <namespace>.svc.cluster.local
    - svc.cluster.local
    - cluster.local
```

The nameserver must be a numeric Service IP; a Service DNS name is not
accepted in `dnsConfig.nameservers`.

Retrofitting this configuration causes one initial StatefulSet rollout.
Afterward, a product implementation could handle member additions/removals by
updating the DNS zone and relay configuration without replacing mongod pods.
The current PoC does not automate that lifecycle.

### 7.2 `lsierant2` CoreDNS implementation

The `lsierant2` relay pod has two containers:

| Container | Image | Function |
|---|---|---|
| `envoy` | `docker.io/envoyproxy/envoy:v1.37-latest` | Raw TCP relay from local `27017` to opposite router `443` |
| `coredns` | Digest-pinned OpenShift platform DNS image | Authoritative answers for the PoC external zone and forwarding for all other names |

The CoreDNS binary reported version `1.13.1`. It listens unprivileged on pod
port `5353`; Service `mc-no-mesh-dns` exposes UDP/TCP `53`.

Corefile:

```text
.:5353 {
  errors
  health :8080
  ready :8181
  hosts /etc/relay/hosts {
    ttl 5
    reload 2s
    fallthrough
  }
  forward . /etc/resolv.conf
  cache 30
}
```

The `hosts` plugin reloads the projected file without restarting mongod.
Unmatched queries are forwarded through the relay pod's normal OpenShift DNS
configuration. The DNS Service ClusterIP must remain stable because changing
it requires changing pod `dnsConfig`.

The deployed zone is a static, three-member table and each cluster has only
one combined DNS/relay pod. Loss of that pod removes both DNS and remote TCP
forwarding. A topology update would need to:

1. create the new per-member Services and retain their ClusterIPs;
2. create and admit the owning Route;
3. issue/propagate a certificate containing the new identity;
4. update the CoreDNS hosts data;
5. update Envoy routing if the destination set changed;
6. wait for projected-volume reload and DNS TTL/cache expiry;
7. add or reconfigure the replica-set member.

DNS hot reload avoids a mongod pod rollout, but it does not move existing
long-lived connections. Drivers, mongod, and Agents reconnect according to
their own retry and pool behavior. "No rollout" is therefore not by itself a
proof of zero-downtime endpoint cutover.

The image choice is acceptable for the PoC but not yet a product decision. A
supported implementation must decide whether it can depend on a release
payload image or needs an operator-owned CoreDNS image.

### 7.3 Why `/etc/hosts` was rejected

- kubelet generates and manages `/etc/hosts`;
- `hostAliases` is the supported API, but changing it replaces pods;
- a ConfigMap `subPath` mount does not receive projected updates;
- mounting over `/etc/hosts` conflicts with kubelet behavior;
- an init container gives only startup-time data;
- a sidecar that rewrites the file is race-prone and unsupported;
- NSS or `LD_PRELOAD` interception is resolver-dependent and too invasive.

## 8. Mongod codebase investigation

This investigation was delegated against `~/mdb/mongo`. The original Google
split-horizon design document could not be exported anonymously and returned
HTTP 401, so the implementation, public MongoDB material, and SERVER-41110
were used instead.

### 8.1 Why "reverse horizons" are not horizons

Existing replica-set horizons:

- are selected from an inbound connection's TLS SNI;
- change `hosts`, `primary`, `me`, and related topology returned in `hello`;
- require all members to define the same set of horizon names;
- require unique member endpoints;
- disallow IP-address horizon hosts;
- do not alter heartbeat, replication, initial-sync, or other outbound
  destinations.

A per-source-cluster outbound destination override is therefore a different
feature. It should not be stored in shared `rs.conf`, because physical
reachability differs by source node even though the logical member identity is
shared by the replica set.

SERVER-41110 proposed a `connectionTarget` concept and was closed Won't Fix.
That history reinforces treating egress routing as node-local/infrastructure
state rather than ordinary horizon metadata.

### 8.2 Technically plausible mongod endpoint override

The ASIO transport already distinguishes:

- logical peer identity: `connector->peer`;
- resolved physical socket endpoint: `connector->resolvedEndpoint`.

TLS construction and SAN verification use the logical peer. In principle, a
node-local map could preserve logical member `B.example:27017` while dialing a
local proxy or remote router IP and port.

Such a feature could remove the custom DNS and source Envoy from some
deployments, but it carries significant correctness risk:

- routing logical member B to physical member C can attribute heartbeat and
  election responses to the wrong member;
- shared wildcard certificates may not detect wrong-member routing;
- startup and `_isSelf` behavior must work when advertised and listening
  endpoints differ;
- modern and legacy connection pools must retain logical identity while
  dialing the override;
- oplog fetchers, initial-sync cloners, dedicated `DBClientConnection`s, and
  other non-pool paths must all honor the mapping;
- changing a route requires targeted invalidation of long-lived connections;
- mixed-version replica sets need defined behavior;
- configuration must be process-local, validated, observable, and
  hot-reloadable.

The recommendation is **not** to require a mongod change for the first product
iteration. The namespace relay/DNS approach uses existing behavior and has
already been validated. A mongod endpoint override is a separate server
design project, not a small extension to horizons.

Stock mongod can also carry an `rs.conf` member endpoint on port `443` while
listening locally on `27017`. If fast local self-identification does not match,
mongod can try the configured endpoint and run `_isSelf`. That requires
deterministic ingress back to the same process and working router hairpin
connectivity. It was a source-level finding, not a behavior tested in the
deployed MongoDB `8.0.5-ent` PoCs.

Relevant server files:

- `src/mongo/transport/asio/asio_transport_layer.cpp`
- `src/mongo/transport/asio/asio_session_impl.cpp`
- `src/mongo/db/repl/split_horizon/split_horizon.cpp`
- `src/mongo/db/repl/repl_set_config.cpp`
- `src/mongo/db/repl/topology_coordinator.cpp`
- `src/mongo/db/repl/isself.cpp`

The current checkout observed during handoff is
`e11b0295598cad17c696286f0dd506d9bd6004c3` on local branch `master`; the exact
earlier subagent checkout was not persisted in the session record. The PoCs
ran MongoDB `8.0.5-ent`, so source-level conclusions must be rechecked against
the corresponding server tag before implementation.

## 9. Automation Agent and temporary `port+1` investigation

This investigation was delegated against `~/mdb/mms-automation`. The analyzed
session snapshot was recorded as branch `ops-manager-8.0`, commit
`9ee0c16cc3`. The checkout has since moved; at handoff time it is
`99d5e655d6d2e3adeae430bb4809689a3c1ef430` on `ops-manager-main`.

### 9.1 Findings

MCK passes `-ephemeralPortOffset=1` in external-hostname and multi-cluster
modes. A temporary mongod therefore normally uses `27018` when the managed
mongod uses `27017`.

The temporary process:

- reuses portions of the managed process configuration, dbPath, and TLS;
- is sometimes started with replica-set metadata retained;
- is never added to `rs.conf`;
- may make outbound connections to normal replica-set members;
- is not contacted by other replica-set members;
- is not contacted by Ops Manager or backup storage services, which use HTTP
  restore URLs rather than MongoDB connections to `port+1`;
- may have authorization, keyFile, or cluster authentication disabled during
  some restore phases.

The reason MCK exposes `port+1` is local Agent behavior. Some Agent health,
metadata, and restore paths connect to:

```text
<ProcessConfig.PreferredHostname>:<normal port + 1>
```

When `PreferredHostname` is an externally configured process name, this local
self-connection leaves the pod through the Service/Route path and comes back
to the temporary mongod. Other code, notably parts of PIT restore and shutdown,
already uses `127.0.0.1:port+1`.

No remote inbound connectivity to the temporary process is required.

The PIT restore path already dials loopback in some cases, but its current
`ConnectToMongod` logic sets `tls.Config.InsecureSkipVerify` for `localhost`
and `127.0.0.1`. The preferred fix must retain the external hostname as TLS
`ServerName` rather than preserve that validation bypass.

### 9.2 Consequence for single-port `443`

A single passthrough listener cannot distinguish:

- normal mongod on `27017`; and
- temporary mongod on `27018`

when both use the same hostname, because SNI has no port. Trying to switch the
Route backend based on health would be unsafe: normal clients could be sent to
a temporary restore mongod whose MongoDB authorization is disabled.

### 9.3 Recommended Agent fix

Separate the temporary process's TCP dial address from its TLS identity:

- dial `127.0.0.1:port+1`;
- keep the managed external hostname as TLS `ServerName`;
- bind the temporary process to loopback where all restore modes permit it;
- remove `port+1` from externally reachable Services only after all ephemeral
  workflows use local dialing.

This avoids adding localhost or pod-IP SANs while preserving hostname
verification.

The change must cover more than the main restore path. Validation should
include:

- snapshot restore;
- point-in-time restore;
- replica-set-mode recovery;
- sharded and config-server restore;
- cluster-ID adjustment;
- oplog replay;
- replica-set rename;
- oplog resizing;
- rolling index and storage-field workflows that use ephemeral mongod;
- outbound heartbeats from temporary replica-set-mode mongod;
- older Agent compatibility.

MCK should gate removal of the backup Service port on an Agent capability or
minimum version. Older Agents must retain the existing exposure during a
mixed-version rollout.

The analyzed Agent checkout was not proven to be the exact source revision of
the Agent binary bundled in the PoCs' `1.6.1` database sidecar. Before removing
`27018`, repeat the path audit against the shipped Agent version and every
supported upgrade combination.

Relevant Agent files:

- `action/helpers.go`
- `restore/restorefrombackup.go`
- `restore/helpers.go`
- `state/processconfig.go`
- `mongoclientservice/mongoclientservice.go`
- `backup/pit-restore/pitrestore/pit-restore.go`
- `util/ephemeralport.go`

Relevant MCK files:

- `controllers/operator/create/create.go`
- `controllers/operator/create/create_test.go`
- `docker/mongodb-kubernetes-init-database/content/agent-launcher.sh`
- `controllers/operator/mongodbshardedcluster_controller.go`
- `controllers/operator/mongodbmultireplicaset_controller.go`

## 10. PoC 1: internal pod-Service identities (`lsierant`)

### 10.1 Topology

- cluster0: one member, `mdb-0-0`
- cluster1: two members, `mdb-1-0` and `mdb-1-1`
- default identities:
  - `mdb-0-0-svc.lsierant.svc.cluster.local:27017`
  - `mdb-1-0-svc.lsierant.svc.cluster.local:27017`
  - `mdb-1-1-svc.lsierant.svc.cluster.local:27017`
- `duplicateServiceObjects: true`
- no `externalAccess.externalDomain`
- cert-manager wildcard SAN: `*.lsierant.svc.cluster.local`
- Envoy image: `docker.io/envoyproxy/envoy:v1.37-latest`
- OpenShift passthrough Route host equals each member's pod-Service FQDN

Normal Kubernetes DNS resolves the duplicated full Service names from any
namespace in each participating cluster. The owning Service selects mongod;
remote duplicates select Envoy.

### 10.2 Last successful validation

On 2026-08-29:

- `MongoDBMultiCluster/mdb` was `Running`;
- topology was one PRIMARY and two SECONDARIES;
- TLS verification succeeded in both relay directions;
- authenticated connections succeeded;
- a fresh write with `writeConcern: {w: 3}` succeeded;
- the written document was read through both cross-cluster relay paths.

### 10.3 Artifacts

```text
~/.copilot/session-state/445e4c78-b250-4d6e-b90e-b5c01b036665/files/mc-envoy-poc/
```

The package contains `apply.sh`, guarded `cleanup.sh`, `README.txt`, and
numbered manifests `00` through `14`.

Reproduction:

```bash
bash ~/.copilot/session-state/445e4c78-b250-4d6e-b90e-b5c01b036665/files/mc-envoy-poc/apply.sh
```

## 11. PoC 2: external process identities and namespace DNS (`lsierant2`)

### 11.1 Topology

- cluster0 external domain: `c0.lsierant2.mc-no-mesh.test`
- cluster1 external domain: `c1.lsierant2.mc-no-mesh.test`
- process identities:
  - `mdb-0-0.c0.lsierant2.mc-no-mesh.test:27017`
  - `mdb-1-0.c1.lsierant2.mc-no-mesh.test:27017`
  - `mdb-1-1.c1.lsierant2.mc-no-mesh.test:27017`
- external per-member Services are ClusterIP `*-svc-external` objects;
- owning Services select mongod;
- remote duplicates select `app=mc-no-mesh-relay`;
- each relay pod contains Envoy and CoreDNS;
- server SANs contain only:
  - `*.c0.lsierant2.mc-no-mesh.test`
  - `*.c1.lsierant2.mc-no-mesh.test`

The `.test` names are intentionally not public DNS. The namespace CoreDNS view
maps each process name to the corresponding local Service ClusterIP. That
Service chooses local mongod or Envoy.

### 11.2 Last successful validation

On 2026-08-29:

- `MongoDBMultiCluster/mdb` was `Running`;
- `MongoDBUser/mc-no-mesh-user` was `Updated`;
- topology was one PRIMARY and two SECONDARIES;
- the external names appeared in replica-set status;
- DNS returned the expected per-cluster Service IPs;
- bidirectional authenticated TLS succeeded;
- a fresh `w:3` write and cross-relay reads succeeded;
- a subsequent operator reconcile retained the exact remote selector.

### 11.3 Artifacts

```text
~/mdb/projects/mc-no-mesh/lsierant2/
```

The package contains `apply.sh`, guarded `cleanup.sh`, and numbered manifests
`00` through `15`.

Reproduction:

```bash
bash /Users/lukasz.sierant/mdb/projects/mc-no-mesh/lsierant2/apply.sh
```

To reuse the built operator image:

```bash
BUILD_OPERATOR_IMAGE=false \
  bash /Users/lukasz.sierant/mdb/projects/mc-no-mesh/lsierant2/apply.sh
```

## 12. Current live state versus last-known-good state

The deployments were **not** cleaned up.

Read-only checks on 2026-09-08 found:

- relay, CoreDNS, API proxy, operator, and client pods still running;
- all mongod container processes still running with zero restarts;
- all mongod containers currently not Ready;
- both `MongoDBMultiCluster/mdb` resources currently `Pending`;
- status message: `StatefulSet not ready`;
- operator logs report that all three Automation Agents have not reached their
  Automation Config goal state;
- Agent logs report Cloud QA `INACTIVE_GROUP`, then `All 0 Mongo processes are
  in goal state`;
- readiness reports `ExpectedToBeUp: true`, `IsInGoalState: true`, but
  `LastMongoUpTime: 2026-08-31 15:01:01 +0000 UTC` and `Mongod is not ready`;
- cert-manager Certificates are Ready and not expired;
- `status.link` remains empty.

Observed CR status:

| Namespace | Generation | Phase | Immediate status reason |
|---|---:|---|---|
| `lsierant` | 6 | `Pending` | `mdb-0` has zero ready/updated pods |
| `lsierant2` | 1 | `Pending` | `mdb-0` has zero ready/updated pods |

The last transition to unready pod state was observed on 2026-08-31; the CR
status transition was 2026-09-06. The common `INACTIVE_GROUP` response and
zero-process Automation Config strongly indicate that the external Cloud QA
projects became inactive and the Agent subsequently stopped managing mongod.
This handoff did not reactivate the projects or restore the deployments. The
current failure does not provide new evidence about the relay data plane.
Do not treat the deployments as healthy until Cloud QA project state and the
Automation Config are restored.

Useful first diagnostics:

```bash
kubectl --context <cluster0> -n <namespace> \
  get mongodbmulticluster.mongodb.com mdb -o json | jq .status
kubectl --context <cluster0> -n <namespace> \
  get events --field-selector involvedObject.name=mdb-0-0 \
  --sort-by=.lastTimestamp
kubectl --context <cluster0> -n <namespace> \
  logs mdb-0-0 --tail=250
```

The earlier `tls-echo` Route experiment was cleaned up; no Deployment,
Service, or Route named `tls-echo` remained on 2026-09-08.

Current Route presence:

| Cluster | `lsierant` | `lsierant2` |
|---|---:|---:|
| cluster0 | 1 | 1 |
| cluster1 | 2 | 2 |

## 13. Operator source changes

Feature branch:

```text
poc/multicluster-envoy-relay
```

Commits:

```text
88d9014e82d8d81b06c04ee03832b172c45753c6
Add remote duplicate pod service overrides

71d3e4f1c25078782b03e4e416809acd99170fbc
Support remote external pod service overrides
```

The branch exists locally and currently points to `71d3e4f1c`. It was not
pushed and no pull request was created. At handoff time the main worktree is
checked out on `master`, not on the PoC branch.

### 13.1 API

The unreleased experimental feature adds:

```yaml
spec:
  duplicateServiceObjects: true
  remoteDuplicatePodService:
    spec:
      selector:
        app: mc-no-mesh-relay
```

The override applies only when:

```text
client cluster != owning cluster
```

This distinction is essential. The same per-member Service is materialized in
every cluster:

- in the owning cluster it must retain the generated mongod selector;
- in a remote cluster its selector must be replaced by the relay selector.

### 13.2 Reconciliation semantics

The implementation:

- handles internal `*-svc` pod Services;
- handles external `*-svc-external` pod Services;
- replaces the remote selector instead of merging it with stale generated
  mongod selector keys;
- preserves immutable/API-assigned Service fields;
- preserves ClusterIP, ClusterIPs, resource version, and allocated NodePorts;
- preserves operator-generated MongoDB ports;
- retains previous behavior when no override is configured.

It only reconciles the Service. It does not deploy, own, health-check, or
report readiness for the selected relay. A `remoteDuplicatePodService`
configuration without a selector does not activate exclusive selector
replacement, and a selector with no endpoints can silently leave remote
member connectivity unavailable. A product API should require a non-empty
selector and surface missing relay endpoints in status.

### 13.3 Changed files

- `api/mongodb/v1/mdbmulti/mongodb_multi_types.go`
- `api/mongodb/v1/mdbmulti/zz_generated.deepcopy.go`
- `config/crd/bases/mongodb.com_mongodbmulticluster.yaml`
- `controllers/operator/mongodbmultireplicaset_controller.go`
- `controllers/operator/mongodbmultireplicaset_controller_test.go`
- `helm_chart/crds/mongodb.com_mongodbmulticluster.yaml`
- `pkg/kube/service/service.go`
- `pkg/kube/service/service_test.go`
- `public/crds.yaml`

The second commit changes only the controller and its tests.

### 13.4 Validation completed at implementation time

```text
make generate manifests
go test ./api/mongodb/v1/mdbmulti ./pkg/kube/service ./controllers/operator -count=1
go vet ./pkg/kube/service ./controllers/operator
git diff --check
```

Focused tests cover:

- local internal and external Service selectors;
- remote internal and external Service replacement;
- behavior without an override;
- stale selector cleanup after repeated reconciliation;
- preservation of immutable and allocated Service fields.

## 14. Client horizon design

The existing PoCs solve member transport but do not expose a normal,
publicly-routable replica-set topology.

A proposed extension is:

```yaml
spec:
  connectivity:
    replicaSetHorizons:
      - client: mdb-0-0.client.example.com:443
      - client: mdb-1-0.client.example.com:443
      - client: mdb-1-1.client.example.com:443
```

Required resources:

1. one unique DNS name per member;
2. each name resolves to the owning cluster's router;
3. one passthrough Route per member;
4. Route host equals the horizon hostname;
5. Route backend is the local owning member Service on `27017`;
6. every horizon hostname is in the mongod certificate SANs;
7. every member defines the same horizon key, `client`.

A normal client URI should include multiple horizon seeds:

```text
mongodb://mdb-0-0.client.example.com:443,mdb-1-0.client.example.com:443,mdb-1-1.client.example.com:443/?replicaSet=mdb&tls=true
```

When the initial TLS SNI matches a configured client horizon hostname, mongod
returns all three client endpoints with port `443`.

Do not use:

- an IP seed;
- a generic alias not present as that member's horizon;
- a TLS-terminating proxy;
- the same hostname for the default endpoint and a second horizon with a
  different port.

Horizon reverse mapping is hostname-based because SNI has no port. Distinct
hostnames avoid ambiguity.

### 14.1 Internal clients

For `lsierant`, clients in any namespace of either participating cluster can
use the default fully qualified `.svc.cluster.local` names because the
Services are duplicated locally.

For `lsierant2`, a client not using `mc-no-mesh-dns` cannot resolve the default
process names. The routable client horizon should therefore be the supported
general-purpose client interface.

One public client horizon can serve internal and external clients if pods can
reach the router through its public/enterprise address. Router hairpin and
egress policy must be validated in both clusters. If that is not possible,
provide a separate internal horizon or an internal DNS view for the client
names.

### 14.2 Current `MongoDBMultiCluster` horizon gaps

The field is already wired into Automation Config:

```text
MongoDBMultiCluster.spec.connectivity
  -> NewMultiClusterReplicaSetWithProcesses
  -> ReplicaSetWithProcesses.SetHorizons
  -> Automation Config replica-set members
```

No mongod code change is required to test it. Production operator gaps remain:

- the horizon array is positional while process ordering is derived from
  `clusterSpecList`;
- scaling or topology changes can shift member-to-horizon mapping;
- `MongoDBMultiCluster` validates the TLS requirement but does not run the
  single-cluster resource's equivalent horizon count and DNS-name validation;
- `MultiReplicaSetConfig` does not pass horizons into certificate-domain
  validation;
- the operator does not create the public DNS records or routable Routes;
- the current PoCs' certificates do not contain client horizon SANs.

A durable API should bind horizon entries to stable process identity rather
than require users to maintain a global positional array.

## 15. Certificate issuance and distribution

Both PoCs used cert-manager in cluster0:

1. create a namespace self-signed issuer;
2. create a namespace CA;
3. issue the mongod server certificate;
4. issue a separate Automation Agent client certificate;
5. copy the server Secret, Agent Secret, and public CA ConfigMap to cluster1.

Cluster1 did not independently issue or renew the certificates. This manual
copy path is PoC-only. Adding a client horizon requires reissuing the server
certificate with every client hostname before enabling the new Routes and
horizon.

A production design must choose and automate one model:

- independent per-cluster issuers under a shared trusted CA;
- controlled cross-cluster Secret replication;
- an external certificate provider available to every cluster.

The lifecycle must cover renewal ordering, CA trust distribution, overlap
during rotation, relay/client connection draining, and member additions. A
certificate must be valid in every cluster before Automation Config starts
advertising the associated identity.

## 16. Other operator investigations

### 16.1 Missing `status.link`

`MongoDBMultiClusterStatus.Link` exists but the legacy multicluster status path
never assigns it:

- the reconciler does not pass a project-link status option;
- `MongoDBMultiCluster.UpdateStatus` does not apply
  `status.BaseUrlOption`.

The expected first-PoC link was:

```text
https://cloud-qa.mongodb.com/v2/6a91ea0de74d992b81f33436
```

The complete fix needs both:

1. apply `BaseUrlOption` to `MongoDBMultiStatus.Link`;
2. pass `deployment.Link(conn.BaseURL(), conn.GroupID())` during successful
   status updates.

No fix was implemented.

### 16.2 Cloud QA organization provenance

Organization ID `6852ed4f903a210ec0c36927` came from the pre-existing
ConfigMap:

```text
cluster0 / mongodb / my-project
```

Cloud QA identifies it as `vivek.s-org`. The PoC scripts copied its `baseUrl`
and `orgId`, changed only the project name, and created separate projects.
They did not create or independently choose the organization.

Kubernetes metadata does not identify who originally created the source
ConfigMap or why that organization was selected.

## 17. PoC-only compatibility mechanisms

Cluster1 did not serve `mongodb.com` CRDs, and the namespace administrator
could not install them. The legacy controller writes cross-cluster owner
references with `blockOwnerDeletion`; cluster1 rejected those references
because it could not resolve the owner GVK.

A namespace-scoped `cluster1-api-proxy` was deployed in cluster0. It:

- authenticates to cluster1 using the namespace ServiceAccount;
- strips owner references from JSON and Kubernetes protobuf writes;
- streams informer watches;
- is protected by a NetworkPolicy allowing only the dedicated operator pod.

This is a PoC compatibility workaround, not part of the networking
architecture.

Other PoC-specific constraints:

- a source-built operator was paired with database/init sidecars at version
  `1.6.1`;
- the StatefulSet supplied
  `MDB_LOG_FILE_AUTOMATION_AGENT_STDERR` for compatibility;
- the dedicated operator was not allowed to replace the shared
  cluster-scoped validating webhook;
- only the new `remoteDuplicatePodService` property was patched into the
  shared MongoDBMultiCluster CRD;
- Ops Manager/Cloud QA remained an external dependency.

## 18. Security observations

- Raw passthrough keeps MongoDB TLS end to end; relay and router do not see
  plaintext.
- Every routable member name must be covered by the mongod certificate.
- Shared wildcard certificates make configuration easier but weaken detection
  of a wrong-member relay mapping. Per-member identity or application-level
  member validation is stronger.
- Remote duplicate Services should select only intended relay pods.
- Relay egress should be restricted to approved router endpoints.
- The API proxy should never become a general-purpose cross-cluster proxy.
- Exposing `port+1` is especially sensitive because temporary restore mongods
  may run with authorization disabled.
- Public client-horizon Routes need normal authentication, authorization,
  network-policy, audit, and rate/capacity controls even though TLS remains
  end to end.

No credentials, Secret values, private keys, or tokens are stored in this
handoff.

## 19. Decisions and recommendations

### Keep

- per-member logical identities;
- duplicated per-member Services;
- owning-Service versus remote-Service selector distinction;
- namespace Envoy raw TCP relay;
- OpenShift passthrough Routes as the tested implementation;
- optional namespace DNS for non-Kubernetes process names;
- separate client horizon on routable `host:443` endpoints.

### Do not make baseline requirements

- service mesh;
- per-member LoadBalancers;
- cluster-wide DNS changes;
- `hostAliases`;
- custom mongod endpoint-routing code;
- external access to Agent temporary `port+1`.

### Productize before release

- generalize routing beyond two clusters;
- make relay and DNS highly available;
- automate DNS, Route, certificate SAN, and relay lifecycle;
- add capability detection for OpenShift Route versus Gateway API;
- make horizon mapping stable across scaling;
- add MongoDBMultiCluster horizon validation and cert checks;
- implement Agent local ephemeral dialing with version/capability gating;
- add observability and failure-mode tests;
- remove the API-proxy dependency by using supported cross-cluster ownership
  and CRD behavior.

## 20. Recommended next execution sequence

1. **Diagnose current PoC health**
   - determine why all database containers became unready and Agents stopped
     reaching Automation Config goal state;
   - restore one PoC to `Running` before extending it.

2. **Run a client-horizon PoC**
   - allocate genuinely routable DNS names;
   - add all client SANs;
   - create one passthrough Route per member;
   - configure a common `client` horizon on port `443`;
   - test a normal replica-set driver from outside both clusters;
   - test a pod that does not use `mc-no-mesh-dns`;
   - assert `hello.hosts` contains only client horizon names with `:443`.
   - force primary failover and verify driver discovery/reconnection.

3. **Harden operator horizon support**
   - replace or validate positional mapping;
   - add multi-cluster count, hostname, uniqueness, and common-key validation;
   - include horizon names in certificate-domain validation;
   - define scaling behavior.

4. **Generalize relays**
   - choose SNI-aware one-relay routing or per-destination relay pools;
   - support more than two clusters;
   - use xDS or a controlled rollout strategy;
   - add HA and metrics.
   - exercise router and relay outages with existing long-lived connections.

5. **Fix Agent ephemeral dialing**
   - separate local socket target from TLS ServerName;
   - convert all ephemeral workflows;
   - test the full restore/maintenance matrix;
   - add capability gating;
   - remove external `27018` only when safe.

6. **Finish secondary defects**
   - implement `MongoDBMultiCluster.status.link`;
   - remove the cluster1 owner-reference proxy workaround in a supported
     deployment model.

7. **Prepare product PRs**
   - split the selector override, horizon hardening, Agent capability, and
     production relay lifecycle into independently reviewable changes;
   - do not treat the PoC branch and manifests as production-ready.

## 21. Artifact and source map

| Purpose | Location |
|---|---|
| Consolidated handoff | `~/mdb/projects/mc-no-mesh/docs/HANDOFF.md` |
| First PoC report | `~/mdb/projects/mc-no-mesh/docs/REPORT.md` |
| Second PoC/DNS report | `~/mdb/projects/mc-no-mesh/docs/LSIERANT2-EXTERNAL-DNS.md` |
| Missing status link | `~/mdb/projects/mc-no-mesh/docs/MDBMC-STATUS-LINK.md` |
| Cloud QA org provenance | `~/mdb/projects/mc-no-mesh/docs/CLOUD-QA-ORG-PROVENANCE.md` |
| First PoC scripts/manifests | `~/.copilot/session-state/445e4c78-b250-4d6e-b90e-b5c01b036665/files/mc-envoy-poc/` |
| Second PoC scripts/manifests | `~/mdb/projects/mc-no-mesh/lsierant2/` |
| Operator feature branch | `poc/multicluster-envoy-relay` |
| MongoDB server checkout | `~/mdb/mongo` |
| Automation Agent checkout | `~/mdb/mms-automation` |

## 22. What was not completed

- No client horizon was deployed.
- No public/enterprise DNS records were created.
- No normal external replica-set driver test was run.
- No primary election or driver failover test was run.
- No member addition/removal or initial-sync test was run.
- No relay/router failure and reconnection test was run.
- No DNS/Route update test with existing long-lived connections was run.
- No certificate rotation test was run.
- No Agent restore or temporary-mongod test was run on the PoCs.
- No Gateway API `TLSRoute` PoC was possible on the inspected cluster.
- No mongod endpoint-override code was written.
- No Automation Agent local-ephemeral-dialing code was written.
- External `port+1` exposure was not removed.
- The `status.link` defect was not fixed.
- The PoC branch was not pushed.
- No pull request was opened.
- The current 2026-09-08 `Pending` state was recorded but not repaired.
