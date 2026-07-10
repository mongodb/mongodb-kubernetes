# AWS multi-cluster sharded-search e2e — architecture & deployment

Single reference for the real-infra (AWS EKS) multi-cluster MongoDBSearch scenario: **what
the deployed architecture looks like** and **what deploys it** (infra scripts + the MCK e2e
test). This is a mesh-free, VPC-peering-free cross-cluster search topology reached entirely
over internet-facing NLBs fronted by L4 SNI-passthrough Envoys.

- Infra provisioning: `scripts/aws/provision_aws_mc_infra.sh` (+ the `mongot_multicluster-infra` repo)
- Cluster-side bootstrap: `scripts/dev/bootstrap_aws_mc_e2e.sh`
- Context: `scripts/dev/contexts/e2e_aws_simulated_mc_sharded`
- Runner: `scripts/dev/e2e_aws_simulated_multi_cluster_sharded.sh`
- Test: `docker/mongodb-kubernetes-tests/tests/multicluster_search/aws_simulated_mc_sharded.py`
  (marker `e2e_aws_simulated_mc_sharded`, manual-only — never wired into Evergreen)
- Data-plane Envoy helper: `docker/mongodb-kubernetes-tests/tests/common/mongod_envoy/mongod_envoy.py`

---

## 1. AWS infrastructure

- **Account** `268558157000`, **region** `eu-south-1`, AWS profile **`mck-admin`**.
- **Four EKS clusters**, each in its own VPC (no peering, no shared mesh). Provisioned by the
  `mongot_multicluster-infra` repo's boto3 orchestrator (`scripts/enterprise/multicluster.py`),
  one manifest per cluster (`manifest-mc-<ctx>.yaml`). EKS cluster name = `test-cluster-<clusterId>`.
- **clusterId** = `<prefix>-mc-<context>`, prefix default `ls` (`MDB_MC_CLUSTER_ID_PREFIX`). So
  contexts `om-mdb-az1`/`mdb-az2`/`search-az1`/`search-az2` → clusterIds `ls-mc-om-mdb-az1`, etc.
- **Node storage**: search nodes are `r5d.xlarge` with instance-store NVMe; the infra runs a
  `local-volume-provisioner` DaemonSet exposing a **`local-nvme`** StorageClass (whole-disk
  ~139Gi PVs). mongot PVCs bind to `local-nvme` (not EBS gp2).
- **In-cluster infra add-ons** (installed by the provisioner): AWS Load Balancer Controller,
  external-dns, cert-manager, the storage classes / NVMe provisioner.
- **Access**: EKS API endpoints are corp-VPN-prefix-locked. The harness authenticates with
  long-lived per-cluster ServiceAccount **bearer tokens** (kube-system SA), not the kubeconfig
  `aws eks get-token` exec.

### Cluster roles (pinned by context name — never by harness sort index)

| Context      | clusterId            | Role                                             |
|--------------|----------------------|--------------------------------------------------|
| `om-mdb-az1` | `ls-mc-om-mdb-az1`   | Central operator + OM/AppDB + shard data **AZ1** + cross-AZ **failover Envoy** |
| `mdb-az2`    | `ls-mc-mdb-az2`      | Shard data **AZ2**                               |
| `search-az1` | `ls-mc-search-az1`   | mongot search, **AZ1** (+ hosts the e2e test pod)|
| `search-az2` | `ls-mc-search-az2`   | mongot search, **AZ2**                           |

> **Index caveat:** the harness SORTS `MEMBER_CLUSTERS`, so harness `cluster_index` is
> alphabetical: `mdb-az2=0, om-mdb-az1=1, search-az1=2, search-az2=3` — **not** declaration
> order. Separately, the operator assigns the **source-internal** cluster index by sorted
> data-cluster name: `mdb-az2 → 0 (AZ2)`, `om-mdb-az1 → 1 (AZ1)`. The two index systems never
> collide (source idx ∈ {0,1}, search idx ∈ {2,3}).

---

## 2. Connectivity model — NLBs + SNI Envoys (no mesh, no peering)

Every `*.svc.cluster.local` cross-cluster hop is replaced by an **external FQDN** behind an
internet-facing NLB. NLBs are created from `LoadBalancer` Services via the AWS LB Controller
(`create_internet_facing_nlb`, `multicluster_utils.py`): `scheme=internet-facing`,
`nlb-target-type=instance`, and — critically — **corp-locked security groups** (never
`0.0.0.0/0`; fail closed). external-dns publishes a wildcard CNAME per NLB.

### LB security groups & ports (provisioned per cluster as `eks-lb-<svc>-sg-<clusterId>`)

| `<svc>` | Port    | Fronts                                                    |
|---------|---------|-----------------------------------------------------------|
| `mongos`| `27017` | data-plane **mongodb-envoy** NLB (source mongod/mongos/AppDB) |
| `mongod`| `27028` | search-managed **Envoy** NLB + the **failover** Envoy NLB (mongod→mongot) |
| `om`    | `443`   | Ops Manager external LB                                    |

Referenced from the Service via `aws-load-balancer-security-groups` using the exact
cluster-suffixed name (`lb_sg()` in the test). `ENVOY_PROXY_PORT = 27028`.

### DNS zones (external-dns)

- Parent zone `mc.mongokubernetes.com`; per-cluster child zone `<clusterId>.mc.mongokubernetes.com`.
- `*.mongodb-proxy.<clusterZone>` → that cluster's **mongodb-envoy** NLB (all MongoDB processes).
- `*.envoy-proxy.<clusterZone>` → that cluster's **search-managed Envoy** NLB.
- `*.envoy-proxy-failover.<centralZone>` → the **cross-AZ failover Envoy** NLB (central cluster).

---

## 3. Envoys

Three Envoy layers. TLS stays **end-to-end** across the L4 ones — they read the ClientHello
SNI with `tls_inspector` and `tcp_proxy`-forward the still-encrypted stream (no certs on the
proxy). Only the operator-managed Envoy terminates TLS.

### (a) Data-plane `mongodb-envoy` — L4 SNI passthrough (`MongodEnvoy`)
One per **data** cluster (`mongodb_envoys` fixture). Fronts every MongoDB process in that
cluster — source shard/config/mongos pods, plus the OM AppDB members (central only) — behind
a single NLB + `*.mongodb-proxy.<clusterZone>` wildcard. The L4 listener demultiplexes by SNI
server-name (pod FQDN); upstreams are the pods' internal headless Services. STRICT_DNS/round-robin.

### (b) Search-managed Envoy — L7 (operator-owned), externally exposed
The operator's managed-LB Envoy terminates mongod's TLS and re-initiates mTLS/gRPC to mongot,
SNI-routing every per-(cluster,shard) and cluster-level FQDN on one `:27028` listener. The
operator creates it as ClusterIP only, so the test adds **one** `LoadBalancer` Service
(`<envoy>-ext`) selecting the Envoy pod + a `*.envoy-proxy.<clusterZone>` wildcard
(`test_expose_search_managed_envoy_externally`). `ENVOY_LB_REPLICAS = 2`.

### (c) Cross-AZ failover Envoy — L4 SNI passthrough (`search_failover_envoy`)
One extra `MongodEnvoy` in the **central** cluster that **round-robins across both** search
clusters' exposed managed-Envoy NLBs (az1 + az2), no health checks, fronted by its own NLB at
`*.envoy-proxy-failover.<centralZone>`. Because L4 cannot rewrite SNI, functional failover
requires the two search clusters to present **shard-unique, cluster-agnostic** SNI server names
(the *same* per-shard hostname in both AZs) so the preserved SNI matches whichever AZ a
connection lands on — see §5.

---

## 4. Source topology & per-AZ sync

- Source sharded MongoDB `mdb-aws-mc-sh`: **2 shards**, spread across the two data clusters.
  Per-shard members **2 in AZ1 / 1 in AZ2**; config RS 2/1; mongos 1 per AZ. In source-index
  order (`SORTED_DATA=[mdb-az2, om-mdb-az1]`) that is `SHARD_MEMBERS_PER_CLUSTER=[1,2]`.
- Source pods carry AZ **tags** (`memberConfig[].tags`, key `CLUSTER_LOCATION_TAG_KEY`); each
  search cluster's MongoDBSearch `syncSourceSelector.matchTagSets` matches its own AZ, so a
  cluster's mongot only syncs from same-AZ shard members (`az1 → search-az1`, `az2 → search-az2`).
- OM/AppDB: OM `om-aws-mc` deployed by the scenario (not cloud-qa), OM 8.0.x; AppDB 3 members
  in the central cluster, mesh-free via external FQDNs on the mongodb-envoy wildcard.
- One `MongoDBSearch` CR **per search cluster** (`per_cluster_mdbs_search`), managed LB mode,
  `MONGOT_REPLICAS_PER_CLUSTER=1`, persistence `local-nvme`/100Gi, mongot image
  `quay.io/mongodb/mongodb-search:1.70.1`.

---

## 5. mongotHost wiring modes (`SOURCE_MONGOT_HOST_MODE`)

The source processes' `mongotHost` (and `searchIndexManagementHostAndPort`) is set by patching
the **OM AutomationConfig** directly (`_wire_source_mongot_host` → `patch_mongot_host_via_ac`),
because there's no per-cluster `additionalMongodConfig` for an external source. Two switchable
modes (resolver is a swappable function):

- **`per_az_direct`** — each process → its OWN-AZ search cluster's managed-Envoy endpoint under
  `*.envoy-proxy.<searchClusterZone>` (`_external_shard_envoy_endpoint` / `_external_mongos_envoy_endpoint`).
- **`failover`** (current default) — every process → a **shard-unique, cluster-agnostic** host
  under the failover wildcard, identical in both AZs (`_failover_shard_proxy_host` /
  `_failover_router_host`, e.g. `mdb-aws-mc-sh-search-<shardName>-proxy-svc.envoy-proxy-failover.<centralZone>:27028`).
  Routed through the failover Envoy, round-robined across both AZs.

**Operator dependency:** failover mode needs the same managed-LB `externalHostname` /
`routerHostname` on **both** clusters' CRs. The operator's original MC validation forbade shared
hostnames across clusters; relaxing that is **PR #1305** (`search: allow shared managed-LB
hostname across clusters`, branch `search/lsierant/relax-mc-hostname-validation`, off `master`).
The `{shardName}` placeholder requirement is retained. The operator image used here is built
from that branch.

---

## 6. TLS / certificates

- cert-manager issues all certs. Source processes get external SANs covering their
  `mongodb-proxy` FQDNs; OM/AppDB similarly.
- Managed-LB server certs (`certs-…-search-lb-<idx>-cert`) carry **dual SANs** — both the
  az-local (`*.envoy-proxy.<searchZone>`) and the failover (`*.envoy-proxy-failover.<centralZone>`)
  hostnames — so flipping `SOURCE_MONGOT_HOST_MODE` needs no cert reissue
  (`_create_external_lb_certificates`).
- Source→mongot hop is enforced mTLS+gRPC via `setParameters` the operator applies and the test
  asserts: `searchTLSMode=requireTLS`, `useGrpcForSearch=true`,
  `skipAuthenticationToMongot=false`, `skipAuthenticationToSearchIndexManagementServer=false`.

---

## 7. Component versions / images

| Component        | Image / version |
|------------------|-----------------|
| Operator         | `268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes:<patch-version_id>` (built from PR #1305 branch) |
| mongot / search  | `quay.io/mongodb/mongodb-search:1.70.1` (`variables/mongodb_search_dev`; matches `release.json .search.version`) |
| Ops Manager/AppDB | OM 8.0.x (`variables/om80`) |
| Envoy (L4 self-managed) | `envoyproxy/envoy:v1.37-latest` |
| Other operator sidecars | `…/staging:latest` (init/database/OM/readiness/upgradeHook) |

Operator image is pinned in the **gitignored** `scripts/dev/contexts/private-context`
(`OPERATOR_REGISTRY=…/dev` + `OPERATOR_VERSION=<patch version_id>`), NOT via
`OVERRIDE_VERSION_ID` (which would wrongly pin all sidecars to the version_id on the staging
registry). The operator install image is delivered through the `operator-installation-config`
ConfigMap (`operator.version`, `search.*`), so an image repin requires re-running the bootstrap.

---

## 8. Deployment

### 8.1 Provision AWS infra — `scripts/aws/provision_aws_mc_infra.sh`
Orchestrates the `mongot_multicluster-infra` boto3 provisioner and writes the merged kubeconfig
(`~/mdb/mongot_multicluster-infra/tmp/search-onprem.kubeconfig`). Phases:

`preflight` → `reset` (clear stale `state-*.json`) → `up` (EKS control planes + node groups,
through cert-manager) → `cpsubnets` (add free-IP public subnet per AZ; fixes the dense central
cluster's IP exhaustion before apiaccess) → `apiaccess` (second pass: cross-cluster NAT
allowlist) → `kubeconfig` (per-cluster `update-kubeconfig`, aliased to short context) → `verify`
(node reachability, needs VPN). `full` also runs `tokens` + `bootstrap`.

```
AWS_PROFILE=mck-admin scripts/aws/provision_aws_mc_infra.sh          # default pipeline
AWS_PROFILE=mck-admin scripts/aws/provision_aws_mc_infra.sh full     # + tokens + bootstrap
```
Teardown: `mongot_multicluster-infra/scripts/enterprise/multicluster.py down` (note: LB-Controller
NLBs/TGs can orphan → VPC DependencyViolation; delete NLBs+TGs first).

### 8.2 SA bearer tokens — `scripts/dev/prepare_aws_mc_tokens.sh`
Extracts per-cluster kube-system SA tokens into `MULTI_CLUSTER_CONFIG_DIR`
(`~/.mck-aws-mc-config`: `central_cluster`, `member_cluster_1..4`, one per context). The harness
reads bearer tokens from here.

### 8.3 Cluster-side bootstrap — `scripts/dev/bootstrap_aws_mc_e2e.sh`
Idempotent (does NOT deploy workloads). Replaces the kind-specific `prepare_local_e2e_run.sh`.
Per cluster: test namespace (`evg=task` label), `image-registries-secret` (ECR pull),
`mongodb-kubernetes-database-pods` / `-appdb` ServiceAccounts (Helm-owned, wired to the pull
secret). Central: the **`operator-installation-config` ConfigMap** (operator + search image
values via `get_operator_helm_values`). test-pod cluster: `test-pod-kubeconfig` secret
(member API-server discovery). **Re-run this after any operator/search image repin.**

### 8.4 Context — `scripts/dev/contexts/e2e_aws_simulated_mc_sharded`
Sources `root-context` + `variables/om80` + `variables/mongodb_search_dev`; forces the AWS
kubeconfig + `MEMBER_CLUSTERS`/`CENTRAL_CLUSTER`/`test_pod_cluster`, `LOCAL_OPERATOR=false`,
`OPERATOR_NAME=mongodb-kubernetes-operator-multi-cluster`, `MDB_MC_CLUSTER_ID_PREFIX=ls`.

### 8.5 Runner — `scripts/dev/e2e_aws_simulated_multi_cluster_sharded.sh`
Self-contained: preloads `.generated/context.env` (for `REGISTRY`), sources the AWS context,
applies the `MCK_DEVC_NET_PREFIX → <ns>-<prefix>` namespace mapping (+ `WATCH_NAMESPACE`), stages
the gitignored `helm_chart` into the test dir (so `helm template` resolves the local chart),
activates the venv, then runs pytest with marker `e2e_aws_simulated_mc_sharded`.

```
MCK_DEVC_NET_PREFIX=1280 AWS_PROFILE=mck-admin \
  scripts/dev/e2e_aws_simulated_multi_cluster_sharded.sh -- [extra pytest args]
```

> These scripts previously depended on the devcontainer layer (`scripts/dev/devenv`, the
> `MCK_DEVC_NET_PREFIX` mapping). This branch is AWS-only and operator-clean; the runner/bootstrap
> reproduce those inline.

---

## 9. e2e test flow (`aws_simulated_mc_sharded.py`, in order)

1. `test_install_central_mc_operator` — central MC operator via Helm (`wait_for_operator_ready`).
2. `test_deploy_mongodb_envoys` — one L4 mongodb-envoy + NLB per data cluster.
3. `test_deploy_ops_manager` — OM + AppDB (mesh-free external FQDNs).
4. `test_install_source_tls_certificates` — source/shard external-SAN certs.
5. `test_create_mdb_source` — the sharded MongoDB source.
6. `test_mongodb_running`, `test_source_spread_across_data_clusters` — shards land per AZ.
7. `test_create_users` — admin / app / mongot users.
8. `test_install_simulated_operators_on_search_clusters` — per-search-cluster operators
   (Helm `--install`, release **`mongodb-kubernetes`**, watches `mongodbsearch` only).
9. `test_create_search_tls_certificates` — managed-LB dual-SAN (az-local + failover) certs.
10. `test_replicate_search_secrets_to_members` — copy CA/user secrets to search clusters.
11. `test_search_cr_reaches_running_per_cluster` — both MongoDBSearch CRs reach **Running**
    (shared failover hostname → requires the relaxed operator, §5).
12. `test_expose_search_managed_envoy_externally` — add the `<envoy>-ext` NLB + wildcard.
13. `test_deploy_search_failover_envoy` — cross-AZ failover Envoy + NLB.
14. `test_per_cluster_sharded_resources_exist` — per-(cluster,shard) mongot STS/Svc/CM/proxy.
15. `test_status_per_cluster_local_only` — per-cluster CR status.
16. `test_shard_sample_collection`, `test_restore_sample_database`, `test_shard_distribution`
    — load + shard `sample_mflix` (**skip on a no-clean rerun** — they duplicate data).
17. `test_wire_source_mongot_host_per_az` — patch OM AC `mongotHost` per `SOURCE_MONGOT_HOST_MODE`.
18. `test_create_search_index_and_query` — create `$search` index + query.
19. `test_search_results_from_all_shards` — deterministic `$search` fan-out across both shards
    (validated live via the **failover** path).
20. `test_create_vector_search_index`, `test_vector_search_query_via_source_mongos` — vector search.
21. `test_cross_cluster_isolation_absence` — negative isolation checks.

---

## 10. End-to-end path (failover mode)

```
                        ┌─────────────────────────────── om-mdb-az1 (central) ───────────────────────────────┐
 $search client         │  MC operator + OM/AppDB + shard data AZ1                                            │
      │                 │                                                                                     │
      ▼                 │   ┌── mongodb-envoy (L4 SNI) ──┐        ┌── search-failover-envoy (L4 SNI) ──┐       │
 mongos (source) ───────┼──►│ *.mongodb-proxy.<az1>      │        │ *.envoy-proxy-failover.<az1>       │       │
   mongotHost =         │   └── upstreams: source pods ──┘        │ round-robin, no health checks       │      │
   *.envoy-proxy-       │                                          └──────┬───────────────┬────────────┘      │
   failover.<az1>:27028 └─────────────────────────────────────────────────┼───────────────┼──────────────────┘
                                                                           ▼               ▼
                                              ┌── search-az1 ──────────────┐   ┌── search-az2 ──────────────┐
                                              │ managed Envoy (L7, mTLS/gRPC)│   │ managed Envoy (L7, mTLS/gRPC)│
                                              │ *.envoy-proxy.<az1>-ext NLB  │   │ *.envoy-proxy.<az2>-ext NLB  │
                                              │        ▼ mongot (1.70.1)     │   │        ▼ mongot (1.70.1)     │
                                              │        NVMe (local-nvme)     │   │        NVMe (local-nvme)     │
                                              └──────────────────────────────┘   └──────────────────────────────┘
      (mdb-az2 mirrors om-mdb-az1's data role for AZ2; its own mongodb-envoy fronts AZ2 source pods)
```

The same shard-unique SNI hostname is presented in both AZs, so the failover Envoy can
round-robin to either search cluster's managed Envoy and the preserved SNI still matches.
