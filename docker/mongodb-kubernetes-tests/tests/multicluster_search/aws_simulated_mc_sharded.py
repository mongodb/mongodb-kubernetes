"""Real-infra (AWS EKS) multi-cluster MongoDBSearch e2e with a SHARDED external source.

Runs against the four real EKS clusters in mongot_multicluster-infra with NO Istio mesh
and NO VPC peering: every cross-cluster hop flows over per-cluster internet-facing NLBs.
The data plane is fronted by a self-managed L4 SNI-passthrough Envoy (mongodEnvoy), one
per data cluster, behind a single NLB + external-dns wildcard. The source sharded
MongoDB is spread across two AZ data clusters, OM is deployed by the scenario, and per-AZ
mongot targeting uses member tagSets. This marker is run manually (not wired into Evergreen);
it requires the corp VPN and the provisioned four-cluster infra.

Cluster roles (pin explicitly; the harness SORTS MEMBER_CLUSTERS, so harness indexes are
alphabetical: mdb-az2=0, om-mdb-az1=1, search-az1=2, search-az2=3 — NOT declaration
order). Never derive a role from a harness sort index.
  - om-mdb-az1  (ls-mc-om-mdb-az1): central operator + OM/AppDB + shard data AZ1
  - mdb-az2     (ls-mc-mdb-az2):    shard data AZ2
  - search-az1  (ls-mc-search-az1): mongot search, AZ1
  - search-az2  (ls-mc-search-az2): mongot search, AZ2

Index model:
  * The source sharded MongoDB spans the TWO data clusters. Its SOURCE-internal cluster
    index is assigned by the operator in SORTED cluster-name order (NOT clusterSpecList
    declaration order — see mongodbshardedcluster_controller.go: slices.Sort before
    AssignIndexesForMemberClusterNames). For our names that means: mdb-az2 -> 0 (AZ2),
    om-mdb-az1 -> 1 (AZ1). MC pod names embed this index: shard mongod
    {mdb}-{shardIdx}-{srcIdx}-{member}, mongos {mdb}-mongos-{srcIdx}-{pod}, config
    {mdb}-config-{srcIdx}-{member}.
  * Per the locked topology each shard has 2 members in AZ1 / 1 in AZ2 (config RS 2/1;
    mongos 1/AZ). In SOURCE-INDEX order that is SHARD_MEMBERS_PER_CLUSTER = [1, 2]
    (idx0=AZ2/mdb-az2=1, idx1=AZ1/om-mdb-az1=2). So a shard's mongod set is
    {mdb}-{shardIdx}-0-0 (AZ2) and {mdb}-{shardIdx}-1-0, {mdb}-{shardIdx}-1-1 (AZ1).
  * The SEARCH side keeps the HARNESS cluster_index (search-az1=2, search-az2=3) for its
    per-(cluster, shard) mongot resource names ({mdbs}-search-{harnessIdx}-{shard}-...).
    The two index systems never collide (source idx in {0,1}, search idx in {2,3}).

Connectivity seams: every ".svc.cluster.local" cross-cluster hop is served instead by an
EXTERNAL FQDN behind an SNI front:
  * source mongod/mongos seeds (mongot syncSource) AND the OM AppDB members -> external
    FQDNs under *.mongodb-proxy.<dataClusterId>.mc... (the shared per-cluster mongodb-envoy NLB).
  * the per-AZ mongotHost endpoints (mongos cluster-level + per-shard) -> external FQDNs
    under *.envoy-proxy.<searchClusterId>.mc... (the operator-managed search Envoy LB).
  * mongodEnvoy upstreams stay INTERNAL (the per-pod headless Services).

The source mongotHost is set by patching the OM Automation Config directly, since the
operator has no per-cluster additionalMongodConfig for an external source. Each source
process points at the mongot co-located in its OWN AZ (no global flip): az1 processes ->
search-az1, az2 processes -> search-az2, so a single $search fans out across both search
clusters by each shard's primary placement.
"""

from __future__ import annotations

import functools
import os
from typing import Dict, List, Optional, Tuple

import kubernetes
import pymongo.errors
import pytest
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
from kubetester.certs import create_ops_manager_tls_certs, create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.common.mongod_envoy.mongod_envoy import MongodEnvoy, SniRoute
from tests.common.mongod_envoy.source_routes import build_replicaset_sni_routes, build_source_sni_routes
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod, get_tools_pod
from tests.common.multicluster.multicluster_utils import (
    assert_workload_ready_in_cluster,
    create_internet_facing_nlb,
    wait_for_nlb_hostname,
)
from tests.common.search import search_resource_names
from tests.common.search.connectivity import CLUSTER_LOCATION_TAG_KEY
from tests.common.search.mc_search_helper import (
    _classify_sharded_process,
    assert_mongot_sync_source_hosts,
    patch_mongot_host_via_ac,
    read_mongod_set_parameter,
    verify_per_cluster_envoy_sni,
)
from tests.common.search.movies_search_helper import (
    EMBEDDING_QUERY_KEY_ENV_VAR,
    EmbeddedMoviesSearchHelper,
    SampleMoviesSearchHelper,
)
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_per_shard_search_tls_certs,
    per_cluster_search_tester,
    verify_search_results_from_all_shards,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_search.conftest import (
    ADMIN_USER_NAME,
    ADMIN_USER_PASSWORD,
    ENVOY_LB_REPLICAS,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    SOURCE_CERT_PREFIX,
    USER_NAME,
    USER_PASSWORD,
    _idx,
    _install_simulated_operator,
    apply_search_crs_and_assert_running,
    assert_cross_cluster_isolation,
    assert_per_cluster_count,
    assert_status_running_local_only,
    create_search_users,
    install_central_mc_operator,
)

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Cluster context names (kubeconfig contexts), pinned by role — never derive role from
# harness sort order.
# ---------------------------------------------------------------------------
CENTRAL_OM_MDB_AZ1 = "om-mdb-az1"
DATA_MDB_AZ2 = "mdb-az2"
SEARCH_AZ1 = "search-az1"
SEARCH_AZ2 = "search-az2"

# Infra-assigned clusterId per context, of the form "<prefix>-mc-<context>" (see the
# mongot_multicluster-infra manifest-mc-*.yaml clusterId fields). The prefix identifies the
# deployment/infra and names + tags every cloud resource (EKS cluster, DNS child zone, LB
# security groups), so it MUST match the infra the run targets — parameterized via
# MDB_MC_CLUSTER_ID_PREFIX (the e2e context exports it; defaults to "ls" for the current infra).
CLUSTER_ID_PREFIX = os.environ.get("MDB_MC_CLUSTER_ID_PREFIX", "ls")
CLUSTER_IDS = {
    ctx: f"{CLUSTER_ID_PREFIX}-mc-{ctx}"
    for ctx in (CENTRAL_OM_MDB_AZ1, DATA_MDB_AZ2, SEARCH_AZ1, SEARCH_AZ2)
}

DATA_CLUSTERS = [CENTRAL_OM_MDB_AZ1, DATA_MDB_AZ2]
SEARCH_CLUSTERS = [SEARCH_AZ1, SEARCH_AZ2]

# Cluster hosting the cross-AZ search failover Envoy. A data cluster (not a search cluster), so
# it survives either search cluster going down; its zone publishes *.envoy-proxy-failover.<zone>.
SEARCH_FAILOVER_HOST_CLUSTER = CENTRAL_OM_MDB_AZ1

# CRITICAL: the operator assigns the SOURCE-internal cluster index by SORTED cluster name
# (controllers/operator/mongodbshardedcluster_controller.go: slices.Sort(allReferencedClusterNames)
# before AssignIndexesForMemberClusterNames), NOT by clusterSpecList declaration order. So for
# our two data clusters the source index is: "mdb-az2" -> 0 (AZ2), "om-mdb-az1" -> 1 (AZ1).
# Every source pod FQDN ({mdb}-{shard}-{srcIdx}-{m}), per-component distribution list (consumed
# positionally by create_tls_certs/cluster_spec_list), and on-disk pod check below is keyed off
# this sorted order. SORTED_DATA[i] is the cluster at source index i.
SORTED_DATA = sorted(DATA_CLUSTERS)  # ["mdb-az2", "om-mdb-az1"] => src idx 0, 1
SOURCE_CLUSTER_INDEX = {ctx: i for i, ctx in enumerate(SORTED_DATA)}

# AZ tag applied to source shard members (memberConfig[].tags) and matched by each search
# cluster's syncSourceSelector so a search cluster's mongot only syncs from its same-AZ
# shard members. Tag key reuses the shared CLUSTER_LOCATION_TAG_KEY (operator renders it to
# mongot syncSource.replicationReader.tagSets).
AZ_BY_DATA_CLUSTER = {CENTRAL_OM_MDB_AZ1: "az1", DATA_MDB_AZ2: "az2"}
AZ_BY_SEARCH_CLUSTER = {SEARCH_AZ1: "az1", SEARCH_AZ2: "az2"}

# Same-AZ search cluster for each data cluster. Each source process queries the mongot
# co-located in its OWN AZ (no global flip): az1 data -> search-az1, az2 data -> search-az2.
SEARCH_FOR_DATA_CLUSTER = {
    data_ctx: next(sc for sc, az in AZ_BY_SEARCH_CLUSTER.items() if az == data_az)
    for data_ctx, data_az in AZ_BY_DATA_CLUSTER.items()
}

PARENT_ZONE = "mc.mongokubernetes.com"

# Corp-prefix-locked LB security groups provisioned by the infra (create_eks_cluster.py, one
# port each). Referenced by the AWS Load Balancer Controller via the
# aws-load-balancer-security-groups annotation so every internet-facing NLB is corp-locked
# (NEVER 0.0.0.0/0 — fail closed). The infra names each SG per-cluster as
# `eks-lb-<svc>-sg-<clusterId>` (create_eks_cluster.py: _ensure_sg(..., f"eks-lb-{svc}-sg-{cluster_id}")),
# so the annotation must carry the cluster-suffixed name — a bare `eks-lb-om-sg` resolves to
# nothing and the controller errors `couldn't find all security groups`. Use lb_sg().
LB_SVC_MONGOS = "mongos"  # :27017 — data-plane mongodEnvoy NLB
LB_SVC_MONGOD = "mongod"  # :27028 — search-managed Envoy LB (mongod -> mongot)
LB_SVC_OM = "om"  # :443  — Ops Manager external LB


def lb_sg(svc: str, context_name: str) -> str:
    """Per-cluster corp-locked LB security-group NAME, as created by the infra.

    The AWS Load Balancer Controller resolves this name within the cluster's VPC; it
    must match the infra's `eks-lb-<svc>-sg-<clusterId>` naming exactly.
    """
    return f"eks-lb-{svc}-sg-{CLUSTER_IDS[context_name]}"


def cluster_zone(context_name: str) -> str:
    """Per-cluster public DNS zone <clusterId>.mc.mongokubernetes.com."""
    return f"{CLUSTER_IDS[context_name]}.{PARENT_ZONE}"


def mongodb_proxy_wildcard(context_name: str) -> str:
    """externalAccess.externalDomain for ALL MongoDB processes in a cluster (AppDB +
    source shard/config/mongos); the external-dns wildcard *.<this> maps to that cluster's
    single mongodb-envoy NLB. Pod FQDN = <pod>.<this>. One wildcard per cluster because pod
    names are globally unique, so one SNI Envoy demultiplexes every process type."""
    return f"mongodb-proxy.{cluster_zone(context_name)}"


def search_proxy_wildcard(context_name: str) -> str:
    """external-dns wildcard parent for a search cluster's managed Envoy NLB."""
    return f"envoy-proxy.{cluster_zone(context_name)}"


def search_failover_wildcard(context_name: str) -> str:
    """external-dns wildcard parent for the cross-AZ search failover Envoy NLB (its host
    cluster's zone). Cluster-agnostic shard-unique proxy hostnames live under this wildcard."""
    return f"envoy-proxy-failover.{cluster_zone(context_name)}"


# ---------------------------------------------------------------------------
# Resource + topology constants
# ---------------------------------------------------------------------------
MDB_RESOURCE_NAME = "mdb-aws-mc-sh"
MDBS_RESOURCE_NAME = "mdb-aws-mc-sh-search"

OM_NAME = "om-aws-mc"
OM_CERT_PREFIX = "om-prefix"
APPDB_CERT_PREFIX = "appdb-prefix"
OM_PROJECT_NAME = "aws-mc-sharded"

# AppDB: single member cluster (om-mdb-az1) today, but deployed mesh-free with external
# FQDNs (externalAccess.externalDomain) so the cross-cluster path is already wired. Its
# cluster-internal index is 0 (only position in the AppDB clusterSpecList); pods are
# {OM_NAME}-db-0-{member}, per-pod headless Service {pod}-svc.
APPDB_MEMBERS = 3

SHARD_COUNT = 2
# Per the locked topology: 2 members in AZ1 / 1 in AZ2 per shard; config RS 2/1; mongos 1/AZ.
# Keyed by CONTEXT NAME (AZ1 = om-mdb-az1, AZ2 = mdb-az2)...
SHARD_MEMBERS_BY_CONTEXT = {CENTRAL_OM_MDB_AZ1: 2, DATA_MDB_AZ2: 1}
CONFIG_MEMBERS_BY_CONTEXT = {CENTRAL_OM_MDB_AZ1: 2, DATA_MDB_AZ2: 1}
MONGOS_BY_CONTEXT = {CENTRAL_OM_MDB_AZ1: 1, DATA_MDB_AZ2: 1}

# ...then projected into SOURCE-INDEX order (SORTED_DATA). Position i = source index i, which is
# exactly what create_tls_certs(replicas_cluster_distribution) and cluster_spec_list consume
# positionally and what the source pod names embed. With SORTED_DATA = [mdb-az2, om-mdb-az1]
# these are [1, 2] (NOT [2, 1]): index 0 = AZ2 (1 member), index 1 = AZ1 (2 members).
SHARD_MEMBERS_PER_CLUSTER: List[int | None] = [SHARD_MEMBERS_BY_CONTEXT[c] for c in SORTED_DATA]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [CONFIG_MEMBERS_BY_CONTEXT[c] for c in SORTED_DATA]
MONGOS_PER_CLUSTER: List[int | None] = [MONGOS_BY_CONTEXT[c] for c in SORTED_DATA]

MONGOT_REPLICAS_PER_CLUSTER = 1

# mongot data on node-local NVMe (r5d.xlarge instance store) via the infra's local-volume
# provisioner StorageClass, not EBS gp2. Local PVs are whole-disk (~139Gi); the request fits within.
MONGOT_STORAGE_CLASS = "local-nvme"
MONGOT_STORAGE_SIZE = "100Gi"

CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 90

# Search setParameters the operator must apply to every source mongod/mongos so the
# mongod -> Envoy -> mongot hop is mTLS + gRPC. Asserted back at runtime, not just set.
SOURCE_SEARCH_SET_PARAMETERS = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "requireTLS",
    "useGrpcForSearch": True,
}

# How the source processes' mongotHost is wired in the OM AutomationConfig (see
# _wire_source_mongot_host). Switchable between:
#   "per_az_direct" — each process -> its OWN-AZ search cluster's managed-Envoy endpoint.
#   "failover"      — every process -> a shard-unique, cluster-AGNOSTIC hostname under the
#                     failover Envoy wildcard, round-robined across both AZs. Functional only
#                     once both search clusters present the same cluster-agnostic SNI server
#                     names + cert SANs (the failover follow-up).
SOURCE_MONGOT_HOST_MODE = "per_az_direct"


# ---------------------------------------------------------------------------
# Role-pinned cluster selection (by context name, never by harness sort index).
# ---------------------------------------------------------------------------


def _mcc(member_cluster_clients: List[MultiClusterClient], context_name: str) -> MultiClusterClient:
    for mcc in member_cluster_clients:
        if mcc.cluster_name == context_name:
            return mcc
    raise AssertionError(f"cluster context {context_name!r} not found among {[m.cluster_name for m in member_cluster_clients]}")


def _data_mccs(member_cluster_clients: List[MultiClusterClient]) -> List[MultiClusterClient]:
    """Data clusters in SOURCE-internal clusterSpecList order (AZ1 then AZ2)."""
    return [_mcc(member_cluster_clients, ctx) for ctx in DATA_CLUSTERS]


def _search_mccs(member_cluster_clients: List[MultiClusterClient]) -> List[MultiClusterClient]:
    return [_mcc(member_cluster_clients, ctx) for ctx in SEARCH_CLUSTERS]


# ---------------------------------------------------------------------------
# External FQDN host builders (the connectivity seams).
# ---------------------------------------------------------------------------


def _source_pod_external_fqdn(pod: str, data_context: str) -> str:
    """External FQDN a peer dials for a source pod (resolves to that data cluster's
    mongodEnvoy NLB; Envoy SNI-routes to the pod's internal Service)."""
    return f"{pod}.{mongodb_proxy_wildcard(data_context)}"


def _shard_host_seeds(shard_idx: int) -> List[str]:
    """A shard's source-mongod EXTERNAL seed hosts (what its mongot syncs from), spanning
    both data clusters. Mirrors the per-shard `hosts` in per_cluster_mdbs_search."""
    seeds: List[str] = []
    for data_context in DATA_CLUSTERS:
        src_idx = SOURCE_CLUSTER_INDEX[data_context]
        n = SHARD_MEMBERS_PER_CLUSTER[src_idx]
        for member_idx in range(n or 0):
            pod = f"{MDB_RESOURCE_NAME}-{shard_idx}-{src_idx}-{member_idx}"
            seeds.append(f"{_source_pod_external_fqdn(pod, data_context)}:27017")
    return seeds


def _router_host_seeds() -> List[str]:
    """The source mongos EXTERNAL seed hosts (one per data cluster)."""
    seeds: List[str] = []
    for data_context in DATA_CLUSTERS:
        src_idx = SOURCE_CLUSTER_INDEX[data_context]
        n = MONGOS_PER_CLUSTER[src_idx]
        for pod_idx in range(n or 0):
            pod = f"{MDB_RESOURCE_NAME}-mongos-{src_idx}-{pod_idx}"
            seeds.append(f"{_source_pod_external_fqdn(pod, data_context)}:27017")
    return seeds


def _external_shard_proxy_template(search_context: str, harness_idx: int) -> str:
    """Managed-LB externalHostname for a search cluster: the per-shard proxy-svc name with
    the {shardName} placeholder retained (operator resolves it per shard via ReplaceAll),
    under the search cluster's external-dns wildcard. Host-only (no port): the operator uses
    it verbatim as the per-shard Envoy SNI server_name, which a TLS ClientHello carries
    without a port. The shard-agnostic mongos SNI is configured separately via routerHostname
    (see _external_mongos_proxy_host)."""
    return (
        f"{MDBS_RESOURCE_NAME}-search-{harness_idx}-{{shardName}}-proxy-svc."
        f"{search_proxy_wildcard(search_context)}"
    )


def _external_mongos_proxy_host(search_context: str, harness_idx: int) -> str:
    """Managed-LB routerHostname for a search cluster: the shard-agnostic cluster-level
    proxy-svc host the mongos reaches, under the external-dns wildcard. Host-only (no port):
    the operator uses it verbatim as the cluster-level Envoy SNI server_name. Must be in the
    managed-LB server cert SANs (see test_create_search_tls_certificates)."""
    return f"{MDBS_RESOURCE_NAME}-search-{harness_idx}-proxy-svc.{search_proxy_wildcard(search_context)}"


def _external_shard_envoy_endpoint(search_context: str, harness_idx: int, shard_idx: int) -> str:
    """A search cluster's per-shard managed-Envoy endpoint (external), the per-shard mongod
    mongotHost target."""
    shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
    return (
        f"{MDBS_RESOURCE_NAME}-search-{harness_idx}-{shard_name}-proxy-svc."
        f"{search_proxy_wildcard(search_context)}:{ENVOY_PROXY_PORT}"
    )


def _external_mongos_envoy_endpoint(search_context: str, harness_idx: int) -> str:
    """A search cluster's cluster-level managed-Envoy endpoint (external), the mongos
    mongotHost target. Host:port — the source mongos automationConfig needs the port; the
    SNI server_name (routerHostname) is the host-only form."""
    return f"{_external_mongos_proxy_host(search_context, harness_idx)}:{ENVOY_PROXY_PORT}"


def _failover_shard_proxy_endpoint(shard_idx: int) -> str:
    """Shard-unique, cluster-AGNOSTIC per-shard mongot endpoint under the failover wildcard:
    the SAME hostname in both AZs (no cluster idx / clusterId), so the SNI a connection carries
    matches whichever AZ the failover Envoy round-robins onto. host:port form."""
    shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
    return (
        f"{MDBS_RESOURCE_NAME}-search-{shard_name}-proxy-svc."
        f"{search_failover_wildcard(SEARCH_FAILOVER_HOST_CLUSTER)}:{ENVOY_PROXY_PORT}"
    )


def _failover_mongos_proxy_endpoint() -> str:
    """Cluster-AGNOSTIC cluster-level mongot endpoint under the failover wildcard (mongos)."""
    return (
        f"{MDBS_RESOURCE_NAME}-search-proxy-svc."
        f"{search_failover_wildcard(SEARCH_FAILOVER_HOST_CLUSTER)}:{ENVOY_PROXY_PORT}"
    )


def _source_mongos_search_tester(
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
    username: str,
    password: str,
    data_context: str = CENTRAL_OM_MDB_AZ1,
) -> SearchTester:
    """SearchTester pinned to a data cluster's source mongos via its EXTERNAL FQDN (default
    AZ1; reachable cross-cluster over the mongodEnvoy NLB — there is no mesh here). TLS +
    issuer CA on."""
    src_idx = SOURCE_CLUSTER_INDEX[data_context]
    pod = f"{MDB_RESOURCE_NAME}-mongos-{src_idx}-0"
    host = f"{_source_pod_external_fqdn(pod, data_context)}:27017"
    return per_cluster_search_tester(host, username, password)


# ---------------------------------------------------------------------------
# Ops Manager (deployed by the scenario, MC mode, single member cluster om-mdb-az1).
# ---------------------------------------------------------------------------


def _om_external_fqdn() -> str:
    return f"{OM_NAME}.{cluster_zone(CENTRAL_OM_MDB_AZ1)}"


def _appdb_pod_external_fqdn(member_idx: int) -> str:
    """External FQDN an AppDB member registers as / its peers dial (resolves via the
    mongodb-proxy wildcard to the shared mongodb-envoy NLB in om-mdb-az1)."""
    pod = f"{OM_NAME}-db-0-{member_idx}"
    return f"{pod}.{mongodb_proxy_wildcard(CENTRAL_OM_MDB_AZ1)}"


@fixture(scope="module")
def appdb_certs(namespace: str, multi_cluster_issuer: str) -> str:
    """AppDB server TLS cert. create_appdb_certs adds the internal per-pod Service FQDNs
    automatically; we add the EXTERNAL pod FQDNs (the externalDomain hostnames the members
    register as and verify TLS against) as additional SANs."""
    return create_appdb_certs(
        namespace,
        multi_cluster_issuer,
        f"{OM_NAME}-db",
        cluster_index_with_members=[(0, APPDB_MEMBERS)],
        cert_prefix=APPDB_CERT_PREFIX,
        additional_domains=[_appdb_pod_external_fqdn(m) for m in range(APPDB_MEMBERS)],
    )


@fixture(scope="module")
def ops_manager_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    """OM server TLS cert covering the external OM FQDN (agents in mdb-az2 dial it over
    the internet-facing :443 LB)."""
    return create_ops_manager_tls_certs(
        multi_cluster_issuer,
        namespace,
        OM_NAME,
        secret_name=f"{OM_CERT_PREFIX}-{OM_NAME}-cert",
        additional_domains=[_om_external_fqdn()],
        api_client=central_cluster_client,
    )


@fixture(scope="module")
def ops_manager(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    custom_version: str,
    custom_appdb_version: str,
    ops_manager_certs: str,
    appdb_certs: str,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBOpsManager:
    """OM + AppDB in MultiCluster topology pinned to the single central cluster
    (om-mdb-az1). The AppDB is deployed mesh-free with external FQDNs
    (externalAccess.externalDomain -> the mongodb-proxy wildcard), fronted by the shared
    per-cluster mongodb-envoy SNI NLB rather than per-pod LoadBalancers — exactly as a
    cross-cluster AppDB would be, so spreading it later is a topology change only. External
    connectivity also exposes OM itself: an internet-facing :443 LB locked to the OM corp SG
    with an external-dns hostname, so the source agents in mdb-az2 can register.

    FLAGGED (see report): OM-on-AWS-no-mesh is the least-validated fixture here. Backups
    are disabled to keep it minimal; OM memory request may need tuning for the om node
    group.
    """
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), name=OM_NAME, namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["version"] = custom_version
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["backup"] = {"enabled": False}
    resource["spec"]["clusterSpecList"] = [{"clusterName": CENTRAL_OM_MDB_AZ1, "members": 1}]

    # AppDB: mesh-free external access. externalAccess.externalDomain publishes external
    # pod FQDNs (<pod>.mongodb-proxy.<clusterZone>); the per-cluster externalService is
    # overridden to ClusterIP so the operator does NOT create per-pod LoadBalancers — the
    # shared per-cluster mongodb-envoy SNI NLB fronts the per-pod <pod>-svc-external instead.
    appdb_external_domain = mongodb_proxy_wildcard(CENTRAL_OM_MDB_AZ1)
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "version": custom_appdb_version,
        # skipHostAliasesCheck: all AppDB members share ONE envoy NLB IP (wildcard
        # *.mongodb-proxy -> single NLB), which defeats the automation agent's IP-based
        # local-process detection (hosts/hostequiv.go IsHostOnLocalMachine): every member
        # FQDN resolves to the same IP, so an agent treats ALL members as "local" and
        # reports >1 process, breaking the readiness wait-step shortcut and deadlocking the
        # OrderedReady bootstrap (member-1 never Ready -> member-2 never created -> RsInit
        # never runs). This flag disables the DNS IP-equivalence fallback, leaving only the
        # exact host==overrideLocalHost match, so each agent owns exactly its own process.
        "agent": {"logLevel": "DEBUG", "startupOptions": {"skipHostAliasesCheck": "true"}},
        "externalAccess": {"externalDomain": appdb_external_domain},
        "clusterSpecList": [
            {
                "clusterName": CENTRAL_OM_MDB_AZ1,
                "members": APPDB_MEMBERS,
                "externalAccess": {
                    "externalDomain": appdb_external_domain,
                    "externalService": {
                        "spec": {
                            "type": "ClusterIP",
                            "ports": [{"name": "mongodb", "port": 27017}],
                        }
                    },
                },
            }
        ],
        "security": {
            "certsSecretPrefix": APPDB_CERT_PREFIX,
            "tls": {"ca": multi_cluster_issuer_ca_configmap},
        },
    }

    resource["spec"]["security"] = {
        "certsSecretPrefix": OM_CERT_PREFIX,
        "tls": {"ca": multi_cluster_issuer_ca_configmap},
    }
    resource["spec"]["externalConnectivity"] = {
        "type": "LoadBalancer",
        "port": 443,
        "annotations": {
            "service.beta.kubernetes.io/aws-load-balancer-type": "external",
            "service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing",
            "service.beta.kubernetes.io/aws-load-balancer-security-groups": lb_sg(LB_SVC_OM, CENTRAL_OM_MDB_AZ1),
            # BYO frontend SG ⇒ the controller stops auto-managing backend (node) SG rules
            # unless this is "true"; without it the NLB can't reach the nodePort and all
            # targets fail health checks (i/o timeout). See lb_sg() note.
            "service.beta.kubernetes.io/aws-load-balancer-manage-backend-security-group-rules": "true",
            "external-dns.alpha.kubernetes.io/hostname": _om_external_fqdn(),
        },
    }
    resource["spec"]["opsManagerURL"] = f"https://{_om_external_fqdn()}:443"

    resource.create_admin_secret(api_client=central_cluster_client)
    try_load(resource)
    return resource


# ---------------------------------------------------------------------------
# Operators.
# ---------------------------------------------------------------------------


@fixture(scope="module")
def central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: dict,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    """Central MC operator on om-mdb-az1: manages OM + the source sharded MongoDB across
    the data clusters (watch_search=True registers the MongoDBSearch field index the
    sharded controller needs every reconcile)."""
    return install_central_mc_operator(
        namespace,
        central_cluster_name,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        watch_search=True,
    )


# ---------------------------------------------------------------------------
# CA + source MongoDB.
# ---------------------------------------------------------------------------


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """Issuer CA written to central + ALL members: data clusters verify their own source
    TLS, and search clusters' mongots verify the source's TLS during sync."""
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
        logger.info(f"CA ConfigMap/Secret {CA_CONFIGMAP_NAME} created in cluster {mcc.cluster_name}")
    return name


def _source_external_san_list(component: str) -> List[str]:
    """External pod FQDNs that the source's per-component TLS cert must carry (certs carry
    EXTERNAL domains only). `component` is "shard", "config", or "mongos"."""
    sans: List[str] = []
    for data_context in DATA_CLUSTERS:
        src_idx = SOURCE_CLUSTER_INDEX[data_context]
        if component == "mongos":
            for pod_idx in range(MONGOS_PER_CLUSTER[src_idx] or 0):
                sans.append(_source_pod_external_fqdn(f"{MDB_RESOURCE_NAME}-mongos-{src_idx}-{pod_idx}", data_context))
        elif component == "config":
            for member_idx in range(CONFIG_SRV_PER_CLUSTER[src_idx] or 0):
                sans.append(_source_pod_external_fqdn(f"{MDB_RESOURCE_NAME}-config-{src_idx}-{member_idx}", data_context))
    return sans


def _shard_external_san_list(shard_idx: int) -> List[str]:
    sans: List[str] = []
    for data_context in DATA_CLUSTERS:
        src_idx = SOURCE_CLUSTER_INDEX[data_context]
        for member_idx in range(SHARD_MEMBERS_PER_CLUSTER[src_idx] or 0):
            sans.append(_source_pod_external_fqdn(f"{MDB_RESOURCE_NAME}-{shard_idx}-{src_idx}-{member_idx}", data_context))
    return sans


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    ca_configmap: str,
) -> MongoDB:
    """MC sharded MongoDB source (TLS+SCRAM) spread 2x3 across the two AZ data clusters,
    with externalAccess.externalDomain per data cluster + the operator-generated external
    Service overridden to ClusterIP (so no per-pod LoadBalancers — the single mongodEnvoy
    NLB fronts everything), and per-shard-member AZ tagSets for same-AZ mongot sync."""
    # Build the clusterSpecList in the operator's SORTED-name order so list position == the
    # source-internal cluster index the operator assigns (and == the positional index the
    # distribution lists + create_tls_certs use). SORTED_DATA = [mdb-az2, om-mdb-az1].
    source_cluster_names = list(SORTED_DATA)

    resource = MongoDB.from_yaml(yaml_fixture("search-q3-mc-sharded.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace)
    resource["spec"]["shardCount"] = SHARD_COUNT

    # Tag each shard member with its AZ so every search cluster's mongot matchTagSets
    # selects only its same-AZ shard members. Tag only shard members (mongot reads shards).
    shard_member_configs = [
        [{"tags": {CLUSTER_LOCATION_TAG_KEY: AZ_BY_DATA_CLUSTER[name]}} for _ in range(count or 0)]
        for name, count in zip(source_cluster_names, SHARD_MEMBERS_PER_CLUSTER)
    ]
    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(
        source_cluster_names, SHARD_MEMBERS_PER_CLUSTER, member_configs=shard_member_configs
    )
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, MONGOS_PER_CLUSTER)

    # externalAccess per component + per cluster: externalDomain publishes external pod
    # FQDNs; spec.type=ClusterIP suppresses the operator's per-pod LoadBalancers (the
    # mongodEnvoy fronts the per-pod internal Services instead).
    resource["spec"]["externalAccess"] = {}
    for component in ("shard", "configSrv", "mongos"):
        for index, data_context in enumerate(source_cluster_names):
            resource["spec"][component]["clusterSpecList"][index]["externalAccess"] = {
                "externalDomain": mongodb_proxy_wildcard(data_context),
                "externalService": {
                    "spec": {
                        "type": "ClusterIP",
                        "ports": [{"name": "mongodb", "port": 27017}],
                    }
                },
            }

    # Same shared-NLB-IP issue as AppDB: shard/configSrv each have 2 members in om-mdb-az1
    # (SHARD_MEMBERS_PER_CLUSTER=[1,2]) behind the one mongodEnvoy NLB, so every same-cluster
    # member FQDN resolves to the same IP and the automation agent's IP-based local-process
    # detection (hostequiv.go IsHostOnLocalMachine) would treat them all as local.
    # skipHostAliasesCheck disables the DNS IP-equivalence fallback, leaving only the exact
    # host==overrideLocalHost match so each agent owns exactly its own process. Harmless on
    # mongos (not a replica set). See the AppDB agent block above for the full rationale.
    for _component in ("shard", "configSrv", "mongos"):
        _agent_cfg = resource["spec"][_component].setdefault("agent", {})
        _agent_cfg.setdefault("startupOptions", {})["skipHostAliasesCheck"] = "true"

    # mongotHost/searchIndexManagementHostAndPort are intentionally ABSENT from the CR
    # spec: routing is set later directly on the OM Automation Config (the flip test), so
    # operator reconciles never clobber the patch. The non-routing search setParameters
    # are applied per component here and asserted back at runtime.
    resource["spec"]["mongos"]["additionalMongodConfig"] = {"setParameter": dict(SOURCE_SEARCH_SET_PARAMETERS)}
    resource["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": dict(SOURCE_SEARCH_SET_PARAMETERS)}

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }

    # Requests-only (no CPU limits): limits throttle the source's CPU-hungry TLS/replset
    # startup, widening the window where mongot races it.
    def _pod_spec(cpu: str, memory: str) -> dict:
        return {
            "podTemplate": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-enterprise-database",
                            "resources": {"requests": {"cpu": cpu, "memory": memory}},
                        }
                    ]
                }
            }
        }

    resource["spec"]["configSrvPodSpec"] = _pod_spec("0.5", "512Mi")
    resource["spec"]["shardPodSpec"] = _pod_spec("0.5", "512Mi")
    resource["spec"]["mongosPodSpec"] = _pod_spec("0.2", "256Mi")
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


# ---------------------------------------------------------------------------
# Users (admin / app / mongot-sync-source). Local builder so usernames map to
# MDB_RESOURCE_NAME without relying on the simulated conftest's request.module fixtures.
# ---------------------------------------------------------------------------


def _build_user(yaml_filename: str, name: str, username: str, namespace: str, central_cluster_client) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user("mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, ADMIN_USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mdb_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user("mongodbuser-mdb-user.yaml", USER_NAME, USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
    )


# ---------------------------------------------------------------------------
# mongodb-envoy (one per data cluster; serves AppDB + source shard/config/mongos via SNI).
# ---------------------------------------------------------------------------


@fixture(scope="module")
def mongodb_envoys(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> Dict[str, MongodEnvoy]:
    """ONE SNI-passthrough Envoy per data cluster (named "mongodb-envoy"), fronting EVERY
    MongoDB process that lives in that cluster behind a single corp-SG-locked internet-facing
    NLB + external-dns wildcard (*.mongodb-proxy.<clusterZone>):

      * the source shard / configSrv / mongos pods (both data clusters), and
      * the OM AppDB members (om-mdb-az1 only — AppDB is single-cluster today).

    All process types share one wildcard and one NLB; the L4 tls_inspector demultiplexes by
    SNI server-name, so one Envoy serves them all. Upstreams are each pod's <pod>-svc-external
    ClusterIP. Deployed BEFORE OM so the wildcard exists when the AppDB members first resolve
    each other; the source routes resolve later (STRICT_DNS re-resolves once those pods exist)."""
    envoys: Dict[str, MongodEnvoy] = {}
    for data_context in DATA_CLUSTERS:
        src_idx = SOURCE_CLUSTER_INDEX[data_context]
        external_domain = mongodb_proxy_wildcard(data_context)
        routes: List[SniRoute] = build_source_sni_routes(
            mdb_name=MDB_RESOURCE_NAME,
            namespace=namespace,
            source_cluster_index=src_idx,
            external_domain=external_domain,
            shard_count=SHARD_COUNT,
            shard_members=SHARD_MEMBERS_PER_CLUSTER[src_idx] or 0,
            config_members=CONFIG_SRV_PER_CLUSTER[src_idx] or 0,
            mongos_members=MONGOS_PER_CLUSTER[src_idx] or 0,
        )
        # AppDB lives only in the central cluster; fold its members into that cluster's Envoy.
        if data_context == CENTRAL_OM_MDB_AZ1:
            routes += build_replicaset_sni_routes(
                name=f"{OM_NAME}-db",
                namespace=namespace,
                cluster_index=0,
                members=APPDB_MEMBERS,
                external_domain=external_domain,
            )
        envoys[data_context] = MongodEnvoy(
            namespace=namespace,
            cluster_id=CLUSTER_IDS[data_context],
            routes=routes,
            name="mongodb-envoy",
            configmap_name="mongodb-envoy-config",
            service_name="mongodb-envoy-svc",
            external_dns_hostname=f"*.{external_domain}",
            lb_security_groups=[lb_sg(LB_SVC_MONGOS, data_context)],
            api_client=_mcc(member_cluster_clients, data_context).api_client,
        )
    return envoys


# ---------------------------------------------------------------------------
# Per-cluster MongoDBSearch CRs (one per search cluster).
# ---------------------------------------------------------------------------


@fixture(scope="module")
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """The SAME MongoDBSearch CR (spec.clusters lists both search clusters) applied to each
    search cluster's API; each simulated operator projects its own per-(cluster, shard)
    slice. The source is the EXTERNAL sharded MongoDB (mongos + per-shard mongod seeds are
    external FQDNs), the managed-LB externalHostname is the search cluster's external Envoy
    FQDN, and syncSourceSelector pins same-AZ sync."""
    search_mccs = _search_mccs(member_cluster_clients)

    router_hosts = _router_host_seeds()
    shards = [
        {"shardName": f"{MDB_RESOURCE_NAME}-{shard_idx}", "hosts": _shard_host_seeds(shard_idx)}
        for shard_idx in range(SHARD_COUNT)
    ]

    clusters_spec = []
    for mcc in search_mccs:
        idx = _idx(mcc)
        ctx = mcc.cluster_name
        clusters_spec.append(
            {
                "name": ctx,
                "index": idx,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "persistence": {"single": {"storage": MONGOT_STORAGE_SIZE, "storageClass": MONGOT_STORAGE_CLASS}},
                "loadBalancer": {
                    "managed": {
                        "replicas": ENVOY_LB_REPLICAS,
                        "externalHostname": _external_shard_proxy_template(ctx, idx),
                        "routerHostname": _external_mongos_proxy_host(ctx, idx),
                    },
                },
                "syncSourceSelector": {"matchTagSets": [{CLUSTER_LOCATION_TAG_KEY: AZ_BY_SEARCH_CLUSTER[ctx]}]},
            }
        )

    results: List[Tuple[MultiClusterClient, MongoDBSearch]] = []
    for mcc in search_mccs:
        mdbs = MongoDBSearch.from_yaml(yaml_fixture("simulated-mc-search.yaml"), name=MDBS_RESOURCE_NAME, namespace=namespace)
        mdbs["spec"]["clusters"] = clusters_spec
        mdbs["spec"]["source"] = {
            "username": MONGOT_USER_NAME,
            "passwordSecretRef": {"name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password", "key": "password"},
            "external": {
                "shardedCluster": {"router": {"hosts": router_hosts}, "shards": shards},
                "tls": {"ca": {"name": ca_configmap}},
            },
        }
        mdbs["spec"]["security"] = {"tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX}}
        mdbs.api = kubernetes.client.CustomObjectsApi(mcc.api_client)
        try_load(mdbs)
        results.append((mcc, mdbs))
    return results


# ---------------------------------------------------------------------------
# tools_pod: run in a DATA cluster (om-mdb-az1) so mongorestore reaches the source mongos.
# ---------------------------------------------------------------------------


@fixture(scope="module")
def tools_pod(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> ToolsPod:
    return get_tools_pod(namespace, api_client=_mcc(member_cluster_clients, CENTRAL_OM_MDB_AZ1).api_client)


# =============================================================================
# Test steps
# =============================================================================


@mark.e2e_aws_simulated_mc_sharded
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.assert_is_running()


@mark.e2e_aws_simulated_mc_sharded
def test_deploy_mongodb_envoys(mongodb_envoys: Dict[str, MongodEnvoy]):
    """Deploy the per-data-cluster mongodb-envoy (SNI NLB + external-dns wildcard) BEFORE OM,
    so the *.mongodb-proxy.<clusterZone> wildcard is published when the AppDB members first
    resolve each other by external FQDN (no mesh). Each Envoy already carries the source
    shard/config/mongos routes too; those upstreams (<pod>-svc-external) appear once the
    source MongoDB deploys later — Envoy STRICT_DNS re-resolves them, so deploying upfront
    is safe."""
    for data_context, envoy in mongodb_envoys.items():
        logger.info(f"Deploying mongodb-envoy in {data_context}")
        envoy.deploy()


@mark.e2e_aws_simulated_mc_sharded
def test_deploy_ops_manager(ops_manager: MongoDBOpsManager):
    """Deploy OM + AppDB on om-mdb-az1 and wait Running. AppDB is mesh-free (external FQDNs
    via the mongodb-proxy wildcard + the central mongodb-envoy). FLAGGED: OM-on-AWS-no-mesh
    is the least-validated piece (external LB exposure, OM memory)."""
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_aws_simulated_mc_sharded
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
):
    """Source per-component TLS certs issued on central; the central MongoDB controller
    replicates them to the data members. Certs carry EXTERNAL domains only (added as
    additional_domains): the operator publishes external process hostnames."""

    def _issue(component_resource: str, secret_name: str, distribution: List[int | None], additional_domains: List[str]):
        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=component_resource,
            replicas_cluster_distribution=distribution,
            additional_domains=additional_domains,
            secret_name=secret_name,
            api_client=central_cluster_client,
        )

    for shard_idx in range(SHARD_COUNT):
        _issue(
            component_resource=f"{MDB_RESOURCE_NAME}-{shard_idx}",
            secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-{shard_idx}-cert",
            distribution=SHARD_MEMBERS_PER_CLUSTER,
            additional_domains=_shard_external_san_list(shard_idx),
        )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-config",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-config-cert",
        distribution=CONFIG_SRV_PER_CLUSTER,
        additional_domains=_source_external_san_list("config"),
    )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-mongos",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-mongos-cert",
        distribution=MONGOS_PER_CLUSTER,
        additional_domains=_source_external_san_list("mongos"),
    )
    logger.info(f"Source sharded per-component TLS certs created with external SANs (prefix={SOURCE_CERT_PREFIX})")


@mark.e2e_aws_simulated_mc_sharded
def test_create_mdb_source(
    mdb: MongoDB,
    ops_manager: MongoDBOpsManager,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Wire the source to the scenario-deployed OM (project ConfigMap + credentials) and
    create it (non-blocking; readiness is asserted in test_mongodb_running)."""
    assert multi_cluster_issuer_ca_configmap is not None, "issuer CA ConfigMap required for OM-API TLS trust"
    mdb.configure(ops_manager, OM_PROJECT_NAME)
    # OM serves its API over TLS (cert-manager). configure() builds the project ConfigMap
    # without a CA reference, so the operator cannot verify OM's server cert when
    # reading/creating the project ("x509: certificate signed by unknown authority").
    # Point the project ConfigMap at the issuer CA via sslMMSCAConfigMap (same as the
    # reference no-mesh OM tests).
    base_url = ops_manager.om_status().get_url()
    assert base_url is not None, "OM status has no baseUrl yet"
    create_or_update_configmap(
        mdb.namespace,
        f"{MDB_RESOURCE_NAME}-config",
        {
            "baseUrl": base_url,
            "projectName": OM_PROJECT_NAME,
            "orgId": "",
            "sslMMSCAConfigMap": multi_cluster_issuer_ca_configmap,
        },
        api_client=central_cluster_client,
    )
    mdb.update()


@mark.e2e_aws_simulated_mc_sharded
def test_mongodb_running(mdb: MongoDB):
    # ignore_errors=True: tolerate transient phase=Failed from controller-runtime cache lag on MC STS Create.
    mdb.assert_reaches_phase(Phase.Running, timeout=2400, ignore_errors=True)


@mark.e2e_aws_simulated_mc_sharded
def test_source_spread_across_data_clusters(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Guard the index model every downstream FQDN depends on: each shard's STS exists with
    the expected member count in BOTH data clusters (source idx 0 = AZ1 / 2 members, idx 1
    = AZ2 / 1 member). Polls: test_mongodb_running returns on the MDB resource's OM-agent
    phase=Running, which can precede the last STS pod's EBS PVC binding."""

    def shards_ready() -> Tuple[bool, str]:
        all_ready = True
        msgs: List[str] = []
        for data_context in DATA_CLUSTERS:
            src_idx = SOURCE_CLUSTER_INDEX[data_context]
            mcc = _mcc(member_cluster_clients, data_context)
            expected_members = SHARD_MEMBERS_PER_CLUSTER[src_idx]
            for shard_idx in range(SHARD_COUNT):
                # MC sharded STS = {mdb}-{shardIdx}-{srcClusterIdx} (MultiShardRsName).
                sts_name = f"{MDB_RESOURCE_NAME}-{shard_idx}-{src_idx}"
                ready = mcc.read_namespaced_stateful_set(sts_name, namespace).status.ready_replicas or 0
                if ready >= (expected_members or 0):
                    msgs.append(f"[{mcc.cluster_name}] {sts_name} ready ({ready}/{expected_members})")
                else:
                    all_ready = False
                    msgs.append(f"[{mcc.cluster_name}] {sts_name} ready={ready} expected>={expected_members}")
        return all_ready, "\n".join(msgs)

    run_periodically(shards_ready, timeout=600, sleep_time=10, msg="source shard STS readiness across data clusters")


@mark.e2e_aws_simulated_mc_sharded
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    """Create users before sim-ops install: AC must propagate to mongod agents while the
    cluster is undisturbed."""
    create_search_users(namespace, central_cluster_client, admin_user, mdb_user, mongot_user)


@mark.e2e_aws_simulated_mc_sharded
def test_install_simulated_operators_on_search_clusters(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    """Install a search-only simulated operator on each SEARCH cluster (by context name —
    NOT member_cluster_clients[:2], whose harness indexes are the data clusters here)."""
    base_helm_args = dict(multi_cluster_operator_installation_config)
    for mcc in _search_mccs(member_cluster_clients):
        operator = _install_simulated_operator(namespace, base_helm_args, mcc)
        operator.assert_is_running()


@mark.e2e_aws_simulated_mc_sharded
def test_create_search_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Per-(cluster, shard) mongot TLS certs + managed-LB certs, issued on central (cert-
    manager runs there). mongot certs carry INTERNAL .svc.cluster.local SANs only; the
    managed-LB SERVER cert carries the EXTERNAL proxy FQDNs the source dials. Resource
    names use the HARNESS cluster_index (search-az1=2, search-az2=3)."""
    search_mccs = _search_mccs(member_cluster_clients)

    for mcc in search_mccs:
        create_per_shard_search_tls_certs(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            prefix=MDBS_TLS_CERT_PREFIX,
            shard_count=SHARD_COUNT,
            mdb_resource_name=MDB_RESOURCE_NAME,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=_idx(mcc),
            api_client=central_cluster_client,
        )
        logger.info(f"Per-shard mongot TLS certs created on central for cluster_index={_idx(mcc)}")

    _create_external_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        search_mccs=search_mccs,
        api_client=central_cluster_client,
    )


def _create_external_lb_certificates(*, namespace, issuer, search_mccs, api_client) -> None:
    """Per-cluster managed-LB (search Envoy) server + client certs. The SERVER cert SANs the
    EXTERNAL per-shard + cluster-level proxy FQDNs (what the source mongod dials as mongotHost);
    the CLIENT cert (Envoy -> mongot, internal) keeps the in-cluster wildcard. Secret names
    carry the cluster index (search-lb-{idx}-cert) to match the operator's per-cluster LB certs."""
    for mcc in search_mccs:
        idx = _idx(mcc)
        ctx = mcc.cluster_name
        server_domains: List[str] = []
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            server_domains.append(
                f"{MDBS_RESOURCE_NAME}-search-{idx}-{shard_name}-proxy-svc.{search_proxy_wildcard(ctx)}"
            )
        server_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc.{search_proxy_wildcard(ctx)}")

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx),
            replicas=1,
            service_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx),
            additional_domains=server_domains,
            secret_name=search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, idx),
            api_client=api_client,
        )
        logger.info(f"[{ctx}] managed-LB server cert (idx={idx}) created with EXTERNAL SANs={server_domains}")

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=f"{search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx)}-client",
            replicas=1,
            service_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx),
            additional_domains=[f"*.{namespace}.svc.cluster.local"],
            secret_name=search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, idx),
            api_client=api_client,
        )
        logger.info(f"[{ctx}] managed-LB client cert (idx={idx}) created with internal SANs")


@mark.e2e_aws_simulated_mc_sharded
def test_replicate_search_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Copy centrally-issued Search Secrets to each search cluster (the simulated operators
    read only their own etcd). Uses the HARNESS cluster_index for the per-shard cert names,
    NOT an enumerate position — the search clusters are harness idx 2/3 here, so the shared
    replicate_sharded_search_secrets_to_members helper (which assumes enumerate==idx) cannot
    be reused as-is."""
    search_mccs = _search_mccs(member_cluster_clients)
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in targets:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)

    _copy(f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password", search_mccs)
    logger.info(f"replicated shared mongot password Secret to {len(search_mccs)} search cluster(s)")

    for mcc in search_mccs:
        idx = _idx(mcc)
        # Per-cluster managed-LB certs (search-lb-{idx}-cert) go only to their own cluster.
        _copy(search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, idx), [mcc])
        _copy(search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, idx), [mcc])
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            _copy(
                search_resource_names.shard_tls_cert_name(
                    MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=idx
                ),
                [mcc],
            )
        logger.info(f"replicated per-cluster LB + per-shard Secrets to {mcc.cluster_name} (cluster_index={idx})")


@mark.e2e_aws_simulated_mc_sharded
def test_search_cr_reaches_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Same CR applied to each search cluster; each simulated operator stamps Running on
    its own slice."""
    apply_search_crs_and_assert_running(per_cluster_mdbs_search)


@mark.e2e_aws_simulated_mc_sharded
def test_expose_search_managed_envoy_externally(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Expose each search cluster's operator-managed Envoy with ONE internet-facing NLB
    (corp-SG-locked + external-dns wildcard) selecting the Envoy pod — mirroring the source
    mongodEnvoy pattern.

    The operator-managed Envoy already SNI-routes every per-(cluster,shard) and cluster-level
    external FQDN (``*-proxy-svc.envoy-proxy.<clusterZone>``) to the right mongot upstream on a
    single :27028 listener. So a SINGLE LoadBalancer Service fronting the Envoy pod, plus one
    ``*.envoy-proxy.<clusterZone>`` external-dns wildcard pointing at it, serves ALL search
    endpoints in the cluster.

    The operator does NOT create a LoadBalancer for the Envoy (its proxy Services are ClusterIP
    upstreams). We add ONE NEW Service of our own (``<envoy>-ext``) selecting the Envoy pod —
    and we do NOT modify any operator-owned resource (no patching the ClusterIP proxy Services
    into LoadBalancers; that both mutates operator state and yields N pending NLBs all selecting
    the same pod). The new Service is created by the test and owned by the test.
    """
    ext_svcs: List[Tuple[MultiClusterClient, str]] = []
    for mcc, _mdbs in per_cluster_mdbs_search:
        envoy_app = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, _idx(mcc))
        ext_svc_name = f"{envoy_app}-ext"
        create_internet_facing_nlb(
            mcc.api_client,
            namespace,
            name=ext_svc_name,
            selector_app=envoy_app,
            port=ENVOY_PROXY_PORT,
            port_name="mongot-grpc",
            security_groups=[lb_sg(LB_SVC_MONGOD, mcc.cluster_name)],
            external_dns_hostname=f"*.{search_proxy_wildcard(mcc.cluster_name)}",
            cluster_id=mcc.cluster_name,
        )
        ext_svcs.append((mcc, ext_svc_name))

    # Block until each NLB is provisioned so the mongotHost flip + createSearchIndex can connect.
    for mcc, ext_svc_name in ext_svcs:
        wait_for_nlb_hostname(mcc.api_client, namespace, ext_svc_name, cluster_id=mcc.cluster_name)


@fixture(scope="module")
def search_failover_envoy(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> MongodEnvoy:
    """One L4 SNI-passthrough Envoy in the central data cluster that round-robins across BOTH
    search clusters' exposed managed-Envoy NLBs (az1 + az2), fronted by its own internet-facing
    NLB at *.envoy-proxy-failover.<centralZone>. Single round-robin upstream, no health checks.

    Functional failover requires the search clusters to present shard-unique, cluster-agnostic
    SNI server names (same per-shard hostname in both AZs) so the preserved SNI matches whichever
    AZ a connection lands on — see _mongot_host_for_process modes."""
    host_ctx = SEARCH_FAILOVER_HOST_CLUSTER
    upstreams = tuple(
        (_external_mongos_proxy_host(mcc.cluster_name, _idx(mcc)), ENVOY_PROXY_PORT)
        for mcc in _search_mccs(member_cluster_clients)
    )
    failover_wildcard = search_failover_wildcard(host_ctx)
    return MongodEnvoy(
        namespace=namespace,
        cluster_id=CLUSTER_IDS[host_ctx],
        routes=[SniRoute(server_name=f"*.{failover_wildcard}", upstreams=upstreams)],
        listen_port=ENVOY_PROXY_PORT,
        name="search-failover-envoy",
        configmap_name="search-failover-envoy-config",
        service_name="search-failover-envoy-svc",
        external_dns_hostname=f"*.{failover_wildcard}",
        lb_security_groups=[lb_sg(LB_SVC_MONGOD, host_ctx)],
        api_client=_mcc(member_cluster_clients, host_ctx).api_client,
    )


@mark.e2e_aws_simulated_mc_sharded
def test_deploy_search_failover_envoy(search_failover_envoy: MongodEnvoy):
    """Deploy the cross-AZ search failover Envoy + its NLB (round-robin over both search
    clusters' managed-Envoy NLBs, TLS passthrough, no health checks for now)."""
    search_failover_envoy.deploy()
    search_failover_envoy.lb_hostname()


@mark.e2e_aws_simulated_mc_sharded
def test_per_cluster_sharded_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each search cluster materialises its own (cluster, shard) mongot STS/Svc/CM/proxy,
    the cluster-level mongos-target proxy svc, and a per-cluster Envoy LB. The mongot
    syncSource seeds are the EXTERNAL per-shard FQDNs; Envoy upstreams stay INTERNAL."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    upstreams_by_idx: Dict[int, List[str]] = {}
    for mcc, mdbs in per_cluster_mdbs_search:
        ci = _idx(mcc)
        sts_ready = {}
        local_mongot_upstreams: List[str] = []
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, ci)
            svc_name = search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name, ci)
            cm_name = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name, ci)
            proxy_svc_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, ci)

            sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
            assert sts.spec.replicas == MONGOT_REPLICAS_PER_CLUSTER, (
                f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, got {sts.spec.replicas}"
            )
            mcc.read_namespaced_service(svc_name, namespace)
            mcc.read_namespaced_service(proxy_svc_name, namespace)
            sts_ready[sts_name] = MONGOT_REPLICAS_PER_CLUSTER
            local_mongot_upstreams.append(f"{svc_name}.{namespace}.svc.cluster.local")
            # mongot syncSource hosts == the EXTERNAL per-shard seed list.
            assert_mongot_sync_source_hosts(mcc, cm_name, namespace, _shard_host_seeds(shard_idx))
            logger.info(
                f"[{mcc.cluster_name}] (cluster_index={ci}, shard={shard_name}): sts={sts_name} "
                f"svc={svc_name} proxy={proxy_svc_name} cm={cm_name}"
            )
        upstreams_by_idx[ci] = local_mongot_upstreams

        mcc.read_namespaced_service(search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, ci), namespace)

        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        assert_workload_ready_in_cluster(mcc, namespace, sts_ready, envoy_dep_name)
        mdbs.assert_lb_status()
        logger.info(f"[{mcc.cluster_name}] envoy_lb={envoy_dep_name} ready; mongot STSs ready: {list(sts_ready)}")

    # SNI anchor: upstream check stays internal (the local mongot Service FQDNs). Omit the
    # helper SNI-FQDN anchor (simulated sharded SNI carries per-shard external FQDNs, not the
    # per-cluster proxy-svc FQDN that anchor checks).
    verify_per_cluster_envoy_sni(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients=[mcc for mcc, _ in per_cluster_mdbs_search],
        expected_upstreams_by_idx=upstreams_by_idx,
    )


@mark.e2e_aws_simulated_mc_sharded
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_status_running_local_only(per_cluster_mdbs_search)


# Each source process queries the mongot co-located in its OWN AZ (no global flip): a process
# in data cluster C uses the search cluster in C's AZ. So a single $search fans out across
# BOTH search clusters — each shard's serving member hits its same-AZ mongot.


def _search_idx_by_ctx(member_cluster_clients: List[MultiClusterClient]) -> Dict[str, int]:
    """Harness cluster_index per search context (search-az1=2, search-az2=3), read from the
    live member-client list rather than hardcoded."""
    return {sc: _idx(_mcc(member_cluster_clients, sc)) for sc in SEARCH_CLUSTERS}


def _resolve_mongot_host_per_az_direct(process_name: str, search_idx: Dict[str, int]) -> Optional[str]:
    """per_az_direct mode: each process -> the managed-Envoy endpoint of the search cluster in
    its OWN AZ (chosen by the process's source cluster index), config servers untouched."""
    classified = _classify_sharded_process(process_name, MDB_RESOURCE_NAME, multi_cluster=True)
    if classified is None:
        return None
    role, cluster_index, shard_index = classified
    search_ctx = SEARCH_FOR_DATA_CLUSTER[SORTED_DATA[cluster_index]]
    idx = search_idx[search_ctx]
    if role == "mongos":
        return _external_mongos_envoy_endpoint(search_ctx, idx)
    assert shard_index is not None
    return _external_shard_envoy_endpoint(search_ctx, idx, shard_index)


def _resolve_mongot_host_failover(process_name: str) -> Optional[str]:
    """failover mode: every process -> a shard-unique, cluster-AGNOSTIC hostname under the
    failover Envoy wildcard (same per-shard host in both AZs), config servers untouched."""
    classified = _classify_sharded_process(process_name, MDB_RESOURCE_NAME, multi_cluster=True)
    if classified is None:
        return None
    role, _cluster_index, shard_index = classified
    if role == "mongos":
        return _failover_mongos_proxy_endpoint()
    assert shard_index is not None
    return _failover_shard_proxy_endpoint(shard_index)


def _wire_source_mongot_host(mdb: MongoDB, member_cluster_clients: List[MultiClusterClient]) -> None:
    """Wire every source process's mongotHost via OM AC, per SOURCE_MONGOT_HOST_MODE. The
    host-resolution mode is a swappable function so we can flip per_az_direct <-> failover."""
    search_idx = _search_idx_by_ctx(member_cluster_clients)
    if SOURCE_MONGOT_HOST_MODE == "failover":
        resolve_host = _resolve_mongot_host_failover
    else:
        resolve_host = functools.partial(_resolve_mongot_host_per_az_direct, search_idx=search_idx)
    patch_mongot_host_via_ac(mdb, resolve_host)


def _search_tls_param_mismatches(params: dict) -> List[str]:
    # automation-mongod.conf serializes bool setParameters as "true"/"false" strings while
    # mongos getParameter returns real bools; coerce both sides so a correct value never
    # reads as a mismatch.
    return [
        f"{k}={params.get(k)!r} expected {v!r}"
        for k, v in SOURCE_SEARCH_SET_PARAMETERS.items()
        if str(params.get(k)).lower() != str(v).lower()
    ]


def _assert_source_routed_per_az(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    """Verify each source process's mongotHost points at its OWN-AZ search cluster: per-shard
    endpoint on every shard mongod (both data clusters), cluster-level endpoint on each mongos."""
    search_idx = _search_idx_by_ctx(member_cluster_clients)

    def shards_on_disk() -> tuple:
        all_ok = True
        msgs: List[str] = []
        for data_context in DATA_CLUSTERS:
            src_idx = SOURCE_CLUSTER_INDEX[data_context]
            search_ctx = SEARCH_FOR_DATA_CLUSTER[data_context]
            s_idx = search_idx[search_ctx]
            mcc = _mcc(member_cluster_clients, data_context)
            for shard_idx in range(SHARD_COUNT):
                expected = _external_shard_envoy_endpoint(search_ctx, s_idx, shard_idx)
                for member_idx in range(SHARD_MEMBERS_PER_CLUSTER[src_idx] or 0):
                    pod_name = f"{MDB_RESOURCE_NAME}-{shard_idx}-{src_idx}-{member_idx}"
                    try:
                        params = read_mongod_set_parameter(pod_name, namespace, mcc.api_client)
                        got_host = params.get("mongotHost", "")
                        got_mgmt = params.get("searchIndexManagementHostAndPort", "")
                        tls_bad = _search_tls_param_mismatches(params)
                        if got_host != expected or got_mgmt != expected or tls_bad:
                            all_ok = False
                            msgs.append(
                                f"[{mcc.cluster_name}] {pod_name}: mongotHost={got_host!r} "
                                f"searchIndexMgmt={got_mgmt!r} expected={expected!r} tls_bad={tls_bad}"
                            )
                        else:
                            msgs.append(f"[{mcc.cluster_name}] {pod_name}: mongotHost={expected} + search TLS params OK")
                    except pymongo.errors.PyMongoError as exc:
                        # Transient while the Automation Config rolls out; poll again.
                        all_ok = False
                        msgs.append(f"[{mcc.cluster_name}] {pod_name}: error reading conf: {exc}")
        return all_ok, "\n".join(msgs)

    run_periodically(
        shards_on_disk, timeout=300, sleep_time=10, msg="source shard mongotHost -> own-AZ search cluster"
    )

    get_params = {"getParameter": 1, "mongotHost": 1, "searchIndexManagementHostAndPort": 1}
    get_params.update({k: 1 for k in SOURCE_SEARCH_SET_PARAMETERS})
    for data_context in DATA_CLUSTERS:
        search_ctx = SEARCH_FOR_DATA_CLUSTER[data_context]
        mongos_expected = _external_mongos_envoy_endpoint(search_ctx, search_idx[search_ctx])
        tester = _source_mongos_search_tester(
            member_cluster_clients, namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, data_context=data_context
        )
        params = tester.client.admin.command(get_params)
        got_host = params.get("mongotHost", "")
        got_mgmt = params.get("searchIndexManagementHostAndPort", "")
        tls_bad = _search_tls_param_mismatches(params)
        assert got_host == mongos_expected and got_mgmt == mongos_expected and not tls_bad, (
            f"[{data_context}] mongos mongotHost={got_host!r} searchIndexMgmt={got_mgmt!r} "
            f"expected {mongos_expected!r}; tls_bad={tls_bad}"
        )
    logger.info("source routed per-AZ: mongos+shards mongotHost + search TLS params confirmed")


@mark.e2e_aws_simulated_mc_sharded
def test_shard_sample_collection(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """Hash-shard the EMPTY sample_mflix.movies before loading data so hashed _id
    distributes docs across both shards at insert time. Skip reshardCollection."""
    admin = _source_mongos_search_tester(member_cluster_clients, namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    ns = "sample_mflix.movies"
    try:
        admin.client.admin.command("enableSharding", "sample_mflix")
    except pymongo.errors.OperationFailure as exc:
        if exc.code != 23 and "already enabled" not in str(exc):  # AlreadyInitialized
            raise
    admin.client["sample_mflix"]["movies"].create_index([("_id", pymongo.HASHED)])
    try:
        admin.client.admin.command("shardCollection", ns, key={"_id": "hashed"}, unique=False)
    except pymongo.errors.OperationFailure as exc:
        if "already sharded" in str(exc) or exc.code == 20:
            logger.info(f"{ns} already sharded")
        else:
            raise
    logger.info(f"{ns} hash-sharded empty (skipping reshardCollection redistribution)")


@mark.e2e_aws_simulated_mc_sharded
def test_restore_sample_database(namespace: str, member_cluster_clients: List[MultiClusterClient], tools_pod: ToolsPod):
    """Restore sample_mflix via the source mongos WITHOUT --drop (the movies collection is
    already hash-sharded empty, so inserts distribute across both shards)."""
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
        drop=False,
    )


@mark.e2e_aws_simulated_mc_sharded
def test_shard_distribution(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """Both shards must hold data — proves the pre-shard-then-load distributed inserts."""
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def check() -> tuple:
        counts = movies.get_shard_document_counts()
        total = sum(counts.values())
        if len(counts) != SHARD_COUNT or not all(c > 0 for c in counts.values()):
            return False, f"not all {SHARD_COUNT} shards non-empty: counts={counts} total={total}"
        if min(counts.values()) <= total * 0.10:
            return False, f"shard balance floor not met: min={min(counts.values())} <= 10% of {total}; counts={counts}"
        return True, f"per-shard counts: {counts} total={total}"

    run_periodically(check, timeout=120, sleep_time=5, msg="per-shard document distribution")


@mark.e2e_aws_simulated_mc_sharded
def test_wire_source_mongot_host_per_az(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    """Wire each source process at the mongot in its OWN AZ and verify the AC applied it:
    az1 processes -> search-az1, az2 processes -> search-az2 (no global flip)."""
    assert_per_cluster_count(_search_mccs(member_cluster_clients))
    _wire_source_mongot_host(mdb, member_cluster_clients)
    _assert_source_routed_per_az(namespace, member_cluster_clients)


@mark.e2e_aws_simulated_mc_sharded
def test_create_search_index_and_query(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Create the $search index and assert the deterministic count via the source mongos;
    per-AZ wiring fans the query out to each shard's co-located search cluster."""
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)
    movies.assert_search_query(retry_timeout=SEARCH_QUERY_RETRY_TIMEOUT)
    logger.info("per-AZ routing: deterministic $search via source mongos OK")


@mark.e2e_aws_simulated_mc_sharded
def test_search_results_from_all_shards(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """Cross-shard completeness (still routed at search-az1): a wildcard $search via the
    source mongos returns total-1 docs, proving EVERY shard's mongot contributes."""
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, USER_NAME, USER_PASSWORD)
    verify_search_results_from_all_shards(tester)


@mark.e2e_aws_simulated_mc_sharded
def test_create_vector_search_index(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """Create a $vectorSearch index on embedded_movies and wait READY (still routed at
    search-az1). Indexing needs no Voyage key; the query is env-gated."""
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.create_vector_search_index()
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_aws_simulated_mc_sharded
def test_vector_search_query_via_source_mongos(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """$vectorSearch via the source mongos in the search-az1 window. embedded_movies is
    never sharded, so it lives on the primary shard and limit=4 against thousands of
    pre-embedded docs returns exactly 4."""
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")

    _assert_source_routed_per_az(namespace, member_cluster_clients)

    limit = 4
    tester = _source_mongos_search_tester(member_cluster_clients, namespace, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)

    query_vector = embedded.generate_query_vector("space exploration")
    doc_count = embedded.count_documents_with_embeddings()
    assert doc_count >= limit, f"only {doc_count} embedded_movies docs have embeddings — need >= {limit}"

    embedded.assert_vector_search_count_eventually(
        query_vector, limit, timeout=SEARCH_QUERY_RETRY_TIMEOUT, msg_prefix="via source mongos: "
    )
    logger.info(f"deterministic $vectorSearch (=={limit}) via source mongos OK")


@mark.e2e_aws_simulated_mc_sharded
def test_cross_cluster_isolation_absence(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each search cluster must NOT contain the OTHER cluster's per-(cluster,shard) Search
    resources — confirms each simulated operator only materialises its own LocalizeToCluster
    slice."""
    shard_names = [f"{MDB_RESOURCE_NAME}-{shard_idx}" for shard_idx in range(SHARD_COUNT)]
    assert_cross_cluster_isolation(namespace, per_cluster_mdbs_search, MDBS_RESOURCE_NAME, shard_names=shard_names)
