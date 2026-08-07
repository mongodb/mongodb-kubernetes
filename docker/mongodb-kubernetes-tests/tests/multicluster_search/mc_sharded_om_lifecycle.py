"""Full MongoDBSearch lifecycle on a 3-cluster hub-and-spoke sharded source with self-hosted Ops Manager.

Hub topology: central MC operator on cluster-1 managing clusters 1-3, self-hosted
Ops Manager 8.0.25 with a multi-cluster AppDB (the metrics forwarder requires
OM >= 8.0.25, so this cannot run on cloud-qa), a 3-shard TLS+SCRAM MultiCluster
sharded MongoDB source, and a MongoDBSearch with three named cluster entries,
managed LB, and the metrics forwarder pointed at the source's OM project.

Routing is per-cluster: each cluster's shard mongods target their own cluster's
per-shard proxy Services and each cluster's mongos its own cluster's router proxy.
Spec setParameter is per-shard and uniform across clusters, so the two routing keys
are wired per process directly in the OM automation config (customer-owned,
external-source semantics); only the four uniform base keys ride the spec.

Lifecycle under test, in order: baseline per-(cluster, shard) fan-out with exact
OM MONGOT host registration and the per-cluster routing observed on every pod,
the $search data plane through every cluster's mongos, trailing-shard removal
(search shard list first, then the source; survivors keep cluster-local routing),
cluster-entry removal (cluster-3 fully swept, its still-running source processes
rewired to a survivor's proxies, while both survivors stay byte-identical and
serving), and the CR-delete tail (OM host deregistration, forwarder teardown,
finalizer release, label sweeps on all three clusters, and the search
setParameters draining out of the still-Running source's automation config once
the customer unwires them — spec keys via the operator, routing keys via the AC).
"""

import json
from copy import deepcopy
from typing import Callable, Dict, List

import kubernetes
import pymongo.errors
import yaml
from cryptography import x509
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_secret, read_secret, try_load
from kubetester.certs import create_tls_certs
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
from tests.common.multicluster.multicluster_utils import (
    assert_deployment_ready_in_cluster,
    assert_workload_ready_in_cluster,
)
from tests.common.search import search_resource_names
from tests.common.search.connectivity import (
    CLUSTER_LOCATION_TAG_KEY,
    mongot_data_pvc_names,
    protected_search_input_uids,
    wait_for_metrics_forwarder_phase,
    wait_for_mongot_pvcs_deleted,
    wait_for_resource_deleted,
    wait_for_search_deleted,
    wait_for_search_owned_resources_deleted,
)
from tests.common.search.mc_search_helper import (
    assert_sharded_mongot_host_observed,
    patch_per_cluster_sharded_mongot_host_via_om,
    remove_mongot_host_via_ac,
)
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_search.conftest import (
    ADMIN_USER_NAME,
    ADMIN_USER_PASSWORD,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    SOURCE_CERT_PREFIX,
    USER_NAME,
    USER_PASSWORD,
    create_search_users,
    install_central_mc_operator,
)
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-sh-om"
MDBS_RESOURCE_NAME = "mdb-mc-sh-om-search"

INITIAL_SHARD_COUNT = 3
REDUCED_SHARD_COUNT = 2

MEMBERS_PER_CLUSTER: List[int | None] = [1, 1, 1]
MONGOS_PER_CLUSTER: List[int | None] = [1, 1, 1]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [1, 1, 1]

CLUSTER_COUNT = len(MEMBERS_PER_CLUSTER)
MONGOT_REPLICAS_PER_CLUSTER = 1
# The trailing entry is removed; its source processes keep running and get rewired
# (direct AC) to the REWIRE_TARGET_INDEX survivor's proxies for the same shard.
REMOVED_CLUSTER_INDEX = 2
REWIRE_TARGET_INDEX = 0

# 9 mongot JVMs share one kind host with OM's JVM and 15 source mongods; the default
# mongot request (cpu=2, memory=4Gi => 2Gi heap) does not fit, and a 1Gi heap serves
# sample_mflix fine.
MONGOT_RESOURCE_REQUESTS = {"cpu": "500m", "memory": "2Gi"}

CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

# setParameter keys wiring the source to mongot. On an external source the operator
# never writes them: the four base keys are customer-set spec (additionalMongodConfig,
# uniform across shards and clusters) and the two routing keys are set per process
# directly in the OM automation config, the only layer that can express per-cluster
# endpoints. The customer unwires both halves on Search delete (see the delete test).
SEARCH_SET_PARAMETER_KEYS = (
    "mongotHost",
    "searchIndexManagementHostAndPort",
    "skipAuthenticationToSearchIndexManagementServer",
    "skipAuthenticationToMongot",
    "searchTLSMode",
    "useGrpcForSearch",
)

BASE_SEARCH_SET_PARAMETER = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "requireTLS",
    "useGrpcForSearch": True,
}

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 60


def _idx(mcc: MultiClusterClient) -> int:
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def _shard_names(shard_count: int) -> List[str]:
    return [f"{MDB_RESOURCE_NAME}-{shard_idx}" for shard_idx in range(shard_count)]


def _wire_routing_via_ac(
    mdb: MongoDB,
    namespace: str,
    cluster_indexes: List[int],
    shard_count: int,
    proxy_cluster_index: Callable[[int], int] | None = None,
) -> None:
    """Direct-AC wiring of the two routing keys: shard mongods -> their cluster's per-shard
    proxy, mongos -> their cluster's router proxy (or ``proxy_cluster_index``'s target).
    Recomputed from the live AC on every call, and blocks until agents reach goal state."""
    patch_per_cluster_sharded_mongot_host_via_om(
        mdb=mdb,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        namespace=namespace,
        shard_count=shard_count,
        cluster_indexes=cluster_indexes,
        envoy_proxy_port=ENVOY_PROXY_PORT,
        multi_cluster=True,
        proxy_cluster_index=proxy_cluster_index,
    )


def _assert_mongos_mongot_host(namespace: str, cluster_index: int, proxy_index: int, timeout: int = 300) -> None:
    """Poll one cluster's mongos via getParameter until it reports the proxy_index cluster's
    router proxy as its effective mongot routing (mongos restarts to apply setParameter)."""
    expected = (
        f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, proxy_index)}:{ENVOY_PROXY_PORT}"
    )
    tester = _per_cluster_mongos_search_tester(namespace, cluster_index, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)

    def check() -> tuple:
        try:
            params = tester.client.admin.command(
                {"getParameter": 1, "mongotHost": 1, "searchIndexManagementHostAndPort": 1}
            )
        except pymongo.errors.PyMongoError as exc:
            return False, f"cluster {cluster_index}: mongos getParameter error: {exc}"
        got = (params.get("mongotHost"), params.get("searchIndexManagementHostAndPort"))
        return got == (expected, expected), f"cluster {cluster_index}: mongos routing {got}, want {expected!r}"

    run_periodically(check, timeout=timeout, sleep_time=5, msg=f"cluster {cluster_index}: mongos mongot routing")


def _assert_routing_observed(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    shard_count: int,
    proxy_cluster_index: Callable[[int], int] | None = None,
) -> None:
    """Effective routing on every pod of the given clusters: each (cluster, shard) mongod's
    on-disk automation config and each cluster's mongos getParameter must report the
    target cluster's proxy endpoints (default target: the pod's own cluster)."""
    target = proxy_cluster_index or (lambda cluster_index: cluster_index)
    assert_sharded_mongot_host_observed(
        mdb=mdb,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        namespace=namespace,
        shard_count=shard_count,
        cluster_indexes=[_idx(mcc) for mcc in member_cluster_clients],
        envoy_proxy_port=ENVOY_PROXY_PORT,
        multi_cluster=True,
        member_api_client_by_cluster={_idx(mcc): mcc.api_client for mcc in member_cluster_clients},
        proxy_cluster_index=proxy_cluster_index,
    )
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        _assert_mongos_mongot_host(namespace, ci, target(ci))


def _assert_lb_cert_proxy_sans(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient, cluster_indexes: List[int]
) -> None:
    """Every cluster's Envoy server cert must carry SAN entries for every cluster's
    per-shard proxy and router proxy FQDNs: with per-cluster routing each cluster's
    mongods/mongos dial their own cluster's proxies over requireTLS, and the
    cluster-removal rewire additionally sends the removed cluster's processes to a
    survivor's proxies."""
    expected_sans = {
        search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, ci) for ci in cluster_indexes
    } | {
        f"{search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, ci)}"
        f".{namespace}.svc.cluster.local"
        for ci in cluster_indexes
        for shard_name in _shard_names(INITIAL_SHARD_COUNT)
    }
    for ci in cluster_indexes:
        secret_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci)
        pem = read_secret(namespace, secret_name, api_client=central_cluster_client)["tls.crt"]
        sans = set(
            x509.load_pem_x509_certificate(pem.encode())
            .extensions.get_extension_for_class(x509.SubjectAlternativeName)
            .value.get_values_for_type(x509.DNSName)
        )
        missing = expected_sans - sans
        assert not missing, f"LB server cert {secret_name} missing proxy SANs: {sorted(missing)}"
        logger.info(f"LB server cert for cluster {ci} covers all {len(expected_sans)} proxy SANs")


def _source_router_hosts(namespace: str) -> List[str]:
    # Per-pod headless Services (`<sts>-<clusterIdx>-<podIdx>-svc`) are reachable
    # cross-cluster via Istio; the per-cluster `<sts>-<clusterIdx>-svc` is not.
    return [
        f"{MDB_RESOURCE_NAME}-mongos-{cluster_idx}-{pod_idx}-svc.{namespace}.svc.cluster.local:27017"
        for cluster_idx, n_mongos in enumerate(MONGOS_PER_CLUSTER)
        if n_mongos
        for pod_idx in range(n_mongos)
    ]


def _source_shard_list(namespace: str, shard_count: int) -> List[dict]:
    return [
        {
            "shardName": shard_name,
            "hosts": [
                f"{shard_name}-{cluster_idx}-0-svc.{namespace}.svc.cluster.local:27017"
                for cluster_idx in range(len(MEMBERS_PER_CLUSTER))
                if MEMBERS_PER_CLUSTER[cluster_idx] is not None
            ],
        }
        for shard_name in _shard_names(shard_count)
    ]


def _expected_mongot_hosts(mdbs: MongoDBSearch, shard_count: int, cluster_indexes: List[int]) -> set:
    shard_names = _shard_names(shard_count)
    hosts: set = set()
    for cluster_index in cluster_indexes:
        hosts |= mdbs.shard_mongot_pod_hostnames(shard_names, cluster_index)
    return hosts


def _shard_sts_names(cluster_index: int, shard_names: List[str]) -> List[str]:
    return [
        search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, cluster_index)
        for shard_name in shard_names
    ]


def _shard_artifact_readers(
    mcc: MultiClusterClient, namespace: str, shard_name: str
) -> Dict[str, Callable[[], object]]:
    ci = _idx(mcc)
    core = mcc.core_v1_api()
    sts = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, ci)
    svc = search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name, ci)
    proxy = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, ci)
    cm = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name, ci)
    secret = search_resource_names.shard_operator_managed_tls_secret_name(MDBS_RESOURCE_NAME, shard_name, ci)
    return {
        f"StatefulSet/{sts}": lambda n=sts: mcc.read_namespaced_stateful_set(n, namespace),
        f"Service/{svc}": lambda n=svc: mcc.read_namespaced_service(n, namespace),
        f"Service/{proxy}": lambda n=proxy: mcc.read_namespaced_service(n, namespace),
        f"ConfigMap/{cm}": lambda n=cm: mcc.read_namespaced_config_map(n, namespace),
        f"Secret/{secret}": lambda n=secret: core.read_namespaced_secret(n, namespace),
    }


def _local_artifact_readers(
    mcc: MultiClusterClient, namespace: str, shard_names: List[str]
) -> Dict[str, Callable[[], object]]:
    """Direct readers for the concrete (kind, name) identities of one cluster's Search
    artifact set: per-(cluster, shard) mongot resources plus the cluster-level proxy
    Service and Envoy Deployment/ConfigMap."""
    ci = _idx(mcc)
    apps = mcc.apps_v1_api()
    readers: Dict[str, Callable[[], object]] = {}
    for shard_name in shard_names:
        readers.update(_shard_artifact_readers(mcc, namespace, shard_name))
    cluster_proxy = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, ci)
    envoy_dep = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
    envoy_cm = search_resource_names.lb_configmap_name(MDBS_RESOURCE_NAME, ci)
    readers[f"Service/{cluster_proxy}"] = lambda: mcc.read_namespaced_service(cluster_proxy, namespace)
    readers[f"Deployment/{envoy_dep}"] = lambda: apps.read_namespaced_deployment(envoy_dep, namespace)
    readers[f"ConfigMap/{envoy_cm}"] = lambda: mcc.read_namespaced_config_map(envoy_cm, namespace)
    return readers


def _reader_uids(readers: Dict[str, Callable[[], object]]) -> Dict[str, str]:
    """UIDs of every captured artifact; reading them doubles as a presence guard."""
    return {what: read().metadata.uid for what, read in readers.items()}


def _customer_input_uids(mcc: MultiClusterClient, namespace: str, cluster_index: int) -> Dict[str, str]:
    """UIDs of the customer-replicated Search inputs on one cluster (must survive cleanup).

    Includes all INITIAL_SHARD_COUNT per-shard certs: they are customer Secrets, so even
    the removed shard's cert must stay untouched by every sweep."""

    def shard_cert(shard_name: str) -> str:
        return search_resource_names.shard_tls_cert_name(
            MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index
        )

    all_shards = _shard_names(INITIAL_SHARD_COUNT)
    return protected_search_input_uids(
        mcc.core_v1_api(),
        namespace,
        shard_cert(all_shards[0]),
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
        CA_CONFIGMAP_NAME,
        additional_secret_names=(
            search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, cluster_index),
            search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, cluster_index),
            *(shard_cert(shard_name) for shard_name in all_shards[1:]),
        ),
    )


def _wait_for_cluster_artifacts_swept(
    mcc: MultiClusterClient, namespace: str, readers: Dict[str, Callable[[], object]], shard_names: List[str]
) -> None:
    """Identity 404-polls for every captured artifact, then PVC reap, then the
    label-scoped emptiness backstop for labeled orphans outside the inventory."""
    for what, read in readers.items():
        wait_for_resource_deleted(read, f"{what} in {mcc.cluster_name}")
    for sts_name in _shard_sts_names(_idx(mcc), shard_names):
        wait_for_mongot_pvcs_deleted(namespace, sts_name, api_client=mcc.api_client)
    wait_for_search_owned_resources_deleted(
        mcc.apps_v1_api(),
        mcc.core_v1_api(),
        namespace,
        MDBS_RESOURCE_NAME,
        where=mcc.cluster_name,
    )


def _forwarder_names(cluster_index: int) -> Dict[str, str]:
    return {
        "deployment": search_resource_names.metrics_forwarder_deployment_name(MDBS_RESOURCE_NAME, cluster_index),
        "config": search_resource_names.metrics_forwarder_configmap_name(MDBS_RESOURCE_NAME, cluster_index),
        "agent_key": search_resource_names.metrics_forwarder_agent_key_secret_name(MDBS_RESOURCE_NAME, cluster_index),
        "ca_cert": search_resource_names.metrics_forwarder_ca_configmap_name(MDBS_RESOURCE_NAME, cluster_index),
    }


def _forwarder_artifact_uids(mcc: MultiClusterClient, namespace: str, cluster_index: int) -> Dict[str, str]:
    """UIDs of the cluster's forwarder artifacts; reading them doubles as a presence guard.

    The CA ConfigMap is excluded: it is only replicated when the project ConfigMap
    carries sslMMSCAConfigMap, and this suite talks to OM over plain HTTP.
    """
    names = _forwarder_names(cluster_index)
    apps = mcc.apps_v1_api()
    core = mcc.core_v1_api()
    return {
        names["deployment"]: apps.read_namespaced_deployment(names["deployment"], namespace).metadata.uid,
        names["config"]: core.read_namespaced_config_map(names["config"], namespace).metadata.uid,
        names["agent_key"]: core.read_namespaced_secret(names["agent_key"], namespace).metadata.uid,
    }


def _wait_for_forwarder_artifacts_deleted(mcc: MultiClusterClient, namespace: str, cluster_index: int) -> None:
    names = _forwarder_names(cluster_index)
    apps = mcc.apps_v1_api()
    core = mcc.core_v1_api()
    for read_fn, what in (
        (lambda: apps.read_namespaced_deployment(names["deployment"], namespace), f"Deployment {names['deployment']}"),
        (lambda: core.read_namespaced_config_map(names["config"], namespace), f"ConfigMap {names['config']}"),
        (lambda: core.read_namespaced_secret(names["agent_key"], namespace), f"Secret {names['agent_key']}"),
        (lambda: core.read_namespaced_config_map(names["ca_cert"], namespace), f"ConfigMap {names['ca_cert']}"),
    ):
        wait_for_resource_deleted(read_fn, f"[{mcc.cluster_name}] metrics forwarder {what}")


def _state_cm_clusters(central_core: CoreV1Api, namespace: str) -> Dict[str, dict]:
    cm = central_core.read_namespaced_config_map(
        search_resource_names.metrics_forwarder_state_configmap_name(MDBS_RESOURCE_NAME), namespace
    )
    return json.loads(cm.data["state"]).get("clusters", {})


def _wait_for_state_cm_converged(
    central_core: CoreV1Api, namespace: str, expected_indexes: Dict[str, int], live_shards: List[str]
) -> None:
    """Poll the forwarder topology-state CM to the converged shape: exactly the expected
    clusters, each pinned to its index with shardReplicas covering exactly the live shards
    and no host deletion still in flight (one-shot equality is wrong mid-deferral).
    Sharded entries populate shardReplicas and leave replicas at 0."""
    want_shard_replicas = {shard_name: MONGOT_REPLICAS_PER_CLUSTER for shard_name in live_shards}

    def converged() -> tuple:
        clusters = _state_cm_clusters(central_core, namespace)
        if set(clusters) != set(expected_indexes):
            return False, f"state CM clusters {sorted(clusters)} != {sorted(expected_indexes)}"
        for name, entry in clusters.items():
            if entry.get("clusterIndex") != expected_indexes[name]:
                return False, f"{name}: clusterIndex={entry.get('clusterIndex')}, want {expected_indexes[name]}"
            if entry.get("shardReplicas") != want_shard_replicas:
                return False, f"{name}: shardReplicas={entry.get('shardReplicas')}, want {want_shard_replicas}"
            if entry.get("pendingHostDeletions") or entry.get("hostDeletionReadyAfter"):
                return False, f"{name}: host deletions still in flight: {entry}"
        return True, f"state CM converged for {sorted(expected_indexes)}"

    run_periodically(converged, timeout=300, sleep_time=5, msg="metrics forwarder state CM convergence")


def _routing_ready_groups(central_core: CoreV1Api, namespace: str) -> List[str]:
    cm = central_core.read_namespaced_config_map(
        search_resource_names.search_state_configmap_name(MDBS_RESOURCE_NAME), namespace
    )
    return json.loads(cm.data["state"]).get("routingReadyMongotGroups", [])


def _per_cluster_mongos_search_tester(
    namespace: str,
    cluster_index: int,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to a specific cluster's mongos via its per-pod headless Service.

    `directConnection=true` keeps the driver from discovering the other clusters' mongos.
    """
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{mongos_host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


def _wait_for_search_results(namespace: str, cluster_index: int, timeout: int = SEARCH_QUERY_RETRY_TIMEOUT) -> None:
    tester = _per_cluster_mongos_search_tester(namespace, cluster_index, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search() -> tuple:
        try:
            results = movies.text_search_movies("Star Wars")
            return bool(results), f"cluster {cluster_index}: $search returned {len(results)} results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"cluster {cluster_index}: $search error: {exc}"

    run_periodically(
        execute_search,
        timeout=timeout,
        sleep_time=5,
        msg=f"cluster {cluster_index}: $search via mongos",
    )


# =============================================================================
# Fixtures
# =============================================================================


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    # Central cluster first (the operator reads from here), then each member cluster
    # so mongot pods can verify the source MongoDB TLS cert.
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
    return name


@fixture(scope="module")
def central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: dict,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    return install_central_mc_operator(
        namespace,
        central_cluster_name,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        member_cluster_names,
        watch_search=True,
    )


@fixture(scope="module")
def om(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
) -> MongoDBOpsManager:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    # get_ops_manager spreads OM over the member clusters; a single OM pod is enough
    # here and preserves the host's pod budget for the 9 mongot JVMs.
    ops_manager["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names[:1], [1])
    ops_manager["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])
    ops_manager.create_admin_secret(api_client=central_cluster_client)
    return ops_manager


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
    om: MongoDBOpsManager,
) -> MongoDB:
    """3-shard MC sharded MongoDB source with TLS+SCRAM, distributed across 3 member clusters."""
    resource = MongoDB.from_yaml(
        yaml_fixture("search-q3-mc-sharded.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    # Point at the in-cluster OM instead of the fixture's cloud-qa my-project/my-credentials.
    resource.configure(om, MDB_RESOURCE_NAME, api_client=central_cluster_client)

    # Tag each shard member nodeLocation=<clusterName> so every cluster's mongot matchTagSets
    # selects its cluster-local shard members (tagSets constrain reads per shard via the router).
    shard_member_configs = [
        [{"tags": {CLUSTER_LOCATION_TAG_KEY: name}} for _ in range(count or 0)]
        for name, count in zip(member_cluster_names, MEMBERS_PER_CLUSTER)
    ]
    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names, MEMBERS_PER_CLUSTER, member_configs=shard_member_configs
    )
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MONGOS_PER_CLUSTER)

    resource["spec"]["shardCount"] = INITIAL_SHARD_COUNT
    # Only the uniform base search params ride the spec (their operator drain on delete is
    # under test); the per-cluster routing keys are wired directly in the OM automation
    # config once the search CR is up (test_create_search_resource).
    resource["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": dict(BASE_SEARCH_SET_PARAMETER)}
    resource["spec"]["mongos"]["additionalMongodConfig"] = {"setParameter": dict(BASE_SEARCH_SET_PARAMETER)}

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDB,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch over the external sharded source, one named entry per member cluster.

    externalHostname starts with {shardName}. so the operator can derive the
    cluster-level proxy-svc FQDN for mongos by stripping that prefix.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "shardedCluster": {
                "router": {"hosts": _source_router_hosts(namespace)},
                "shards": _source_shard_list(namespace, INITIAL_SHARD_COUNT),
            },
            "tls": {"ca": {"name": ca_configmap}},
        },
    }

    resource["spec"]["security"] = {
        "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
    }

    clusters = []
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        clusters.append(
            {
                "name": mcc.cluster_name,
                "index": ci,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "resourceRequirements": {"requests": dict(MONGOT_RESOURCE_REQUESTS)},
                "loadBalancer": {
                    "managed": {
                        "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                            MDBS_RESOURCE_NAME, namespace, ci
                        ),
                        # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc
                        # FQDN (matches the LB cert SAN). Distinct per cluster via the cluster index.
                        "routerHostname": search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, ci),
                    },
                },
                "syncSourceSelector": {"matchTagSets": [{CLUSTER_LOCATION_TAG_KEY: mcc.cluster_name}]},
            }
        )
    resource["spec"]["clusters"] = clusters

    # External sources can't auto-resolve the Ops Manager project, so the forwarder is
    # given the source's project ConfigMap and agent-credentials Secret explicitly.
    # Instantiated after the source is Running, so status.projectId is set.
    mdb.load()
    resource["spec"]["observability"] = {
        "prometheus": {"mode": "enabled"},
        "metricsForwarder": {
            "opsManager": {
                "projectConfigMapRef": {"name": mdb.config_map_name},
                "agentCredentials": {"name": f"{mdb['status']['projectId']}-group-secret"},
            },
        },
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


# =============================================================================
# Test steps
# =============================================================================


@mark.e2e_search_mc_sharded_om_lifecycle
def test_install_operator(central_mc_operator: Operator):
    central_mc_operator.wait_for_operator_ready()


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_ops_manager(om: MongoDBOpsManager):
    om.update()
    om.om_status().assert_reaches_phase(Phase.Running, timeout=1500)
    om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
):
    """Source MongoDB per-component TLS certs — written to central; operator copies to members.

    ShardedCluster with certsSecretPrefix expects one secret per component, not a bundle:
    `{prefix}-{resource}-{N}-cert` per shard, plus `-config-cert` and `-mongos-cert`.
    Each cert SANs every member-cluster cross-cluster pod FQDN.
    """

    def _issue(component_resource: str, secret_name: str, distribution: List[int | None]):
        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=component_resource,
            replicas_cluster_distribution=distribution,
            secret_name=secret_name,
            api_client=central_cluster_client,
        )

    for shard_name in _shard_names(INITIAL_SHARD_COUNT):
        _issue(shard_name, f"{SOURCE_CERT_PREFIX}-{shard_name}-cert", MEMBERS_PER_CLUSTER)
    _issue(
        f"{MDB_RESOURCE_NAME}-config",
        f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-config-cert",
        CONFIG_SRV_PER_CLUSTER,
    )
    _issue(
        f"{MDB_RESOURCE_NAME}-mongos",
        f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-mongos-cert",
        MONGOS_PER_CLUSTER,
    )


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_mdb_source(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    create_search_users(namespace, central_cluster_client, admin_user, mdb_user, mongot_user)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_search_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Issue per-(cluster, shard) mongot certs + LB certs on central.

    cert-manager is installed only on the central cluster, so issuing per-cluster
    Certificate CRs on members would 404. Secrets land on central; the next step
    replicates them to each member.
    """
    for i in range(len(member_cluster_clients)):
        create_per_shard_search_tls_certs(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            prefix=MDBS_TLS_CERT_PREFIX,
            shard_count=INITIAL_SHARD_COUNT,
            mdb_resource_name=MDB_RESOURCE_NAME,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=i,
            api_client=central_cluster_client,
        )

    # One Envoy server+client cert per cluster index, each SANing every cluster's
    # per-shard and router proxy FQDNs (the endpoints mongods/mongos dial over requireTLS).
    create_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        shard_count=INITIAL_SHARD_COUNT,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        cluster_indexes=list(range(len(member_cluster_clients))),
        api_client=central_cluster_client,
    )
    _assert_lb_cert_proxy_sans(namespace, central_cluster_client, list(range(len(member_cluster_clients))))


@mark.e2e_search_mc_sharded_om_lifecycle
def test_replicate_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Copy centrally-issued Secrets to each member cluster.

    The MongoDBSearch controller does NOT auto-replicate Secrets (intentional design;
    customer-replicated). The MongoDB sharded controller replicates source certs, but
    Search's own prefix is not covered. Without this step mongot pods on members can't
    mount their TLS material and stay PodInitializing.
    """
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, mcc: MultiClusterClient) -> None:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)

    # Mongot password — same copy to every member cluster.
    password_secret = f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password"
    for mcc in member_cluster_clients:
        _copy(password_secret, mcc)

    # Per-cluster Secrets — LB certs + per-(cluster, shard) mongot certs go to their owning cluster.
    for i, mcc in enumerate(member_cluster_clients):
        _copy(search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, i), mcc)
        _copy(search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, i), mcc)
        for shard_name in _shard_names(INITIAL_SHARD_COUNT):
            _copy(
                search_resource_names.shard_tls_cert_name(
                    MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=i
                ),
                mcc,
            )
        logger.info(f"Replicated per-cluster Secrets to {mcc.cluster_name} (cluster_index={i})")


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_search_resource(
    namespace: str,
    mdb: MongoDB,
    mdbs: MongoDBSearch,
    member_cluster_clients: List[MultiClusterClient],
):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
    # With the proxies up, the customer wires the routing keys per process in the OM AC so
    # every cluster's mongods and mongos target their own cluster's proxy Services.
    _wire_routing_via_ac(mdb, namespace, [_idx(mcc) for mcc in member_cluster_clients], INITIAL_SHARD_COUNT)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_baseline_topology(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every (cluster, shard) pair must have a mongot StatefulSet, headless Service, proxy
    Service, and ConfigMap carrying that cluster's matchTagSets as replicationReader.tagSets;
    every cluster must additionally expose the labelled cluster-level proxy Service (mongos
    routing) and a ready Envoy Deployment, and every pod must observe its own cluster's
    proxy endpoints as its effective mongot routing."""
    assert (
        len(member_cluster_clients) == CLUSTER_COUNT
    ), f"expected {CLUSTER_COUNT} member clusters, got {len(member_cluster_clients)}"
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        for shard_name in _shard_names(INITIAL_SHARD_COUNT):
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, ci)
            svc_name = search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name, ci)
            cm_name = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name, ci)
            proxy_svc_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, ci)

            mcc.read_namespaced_stateful_set(sts_name, namespace)
            mcc.read_namespaced_service(svc_name, namespace)
            mcc.read_namespaced_service(proxy_svc_name, namespace)
            cm = mcc.read_namespaced_config_map(cm_name, namespace)

            config_yaml = cm.data.get("config.yml") or cm.data.get("mongot.yaml")
            assert config_yaml, f"mongot CM {cm_name} missing config payload; data keys={list(cm.data or {})}"
            rr = yaml.safe_load(config_yaml).get("syncSource", {}).get("replicationReader")
            expected_tag_sets = [[{"name": CLUSTER_LOCATION_TAG_KEY, "value": mcc.cluster_name}]]
            assert rr is not None, f"mongot CM {cm_name} in {mcc.cluster_name}: syncSource.replicationReader absent"
            assert rr.get("readPreference") != "primary", (
                f"mongot CM {cm_name} in {mcc.cluster_name}: readPreference is 'primary' "
                "(tagSets require a non-primary read preference)"
            )
            assert rr.get("tagSets") == expected_tag_sets, (
                f"mongot CM {cm_name} in {mcc.cluster_name}: replicationReader.tagSets "
                f"{rr.get('tagSets')!r} != expected {expected_tag_sets!r}"
            )

        cluster_proxy_name = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, ci)
        labels = mcc.read_namespaced_service(cluster_proxy_name, namespace).metadata.labels or {}
        assert labels.get("component") == "search-proxy", (
            f"[{mcc.cluster_name}] cluster-level proxy Service {cluster_proxy_name} missing "
            f"component=search-proxy label; got {labels}"
        )

        envoy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, cluster_index=ci)
        assert_deployment_ready_in_cluster(mcc.apps_v1_api(), name=envoy_name, namespace=namespace)
        logger.info(f"[{mcc.cluster_name}] baseline topology verified (cluster_index={ci})")

    _assert_routing_observed(namespace, mdb, member_cluster_clients, INITIAL_SHARD_COUNT)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_metrics_forwarder_baseline(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
    mdbs: MongoDBSearch,
    member_cluster_clients: List[MultiClusterClient],
):
    """Non-vacuous baseline: forwarder Running on all three clusters with every (cluster,
    shard) mongot pod registered as a MONGOT host in OM and the topology-state CM tracking
    exactly the 3x3 shardReplicas shape — otherwise the removal tests would pass trivially."""
    wait_for_metrics_forwarder_phase(mdbs, Phase.Running, timeout=300)
    mdbs.assert_cluster_statuses(expected_count=CLUSTER_COUNT, expect_managed_lb=True, expect_metrics_forwarder=True)

    shard_names = _shard_names(INITIAL_SHARD_COUNT)
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        assert_workload_ready_in_cluster(
            mcc,
            namespace,
            {sts_name: MONGOT_REPLICAS_PER_CLUSTER for sts_name in _shard_sts_names(ci, shard_names)},
            _forwarder_names(ci)["deployment"],
            timeout=300,
        )

    mdbs.load()
    mdb.get_om_tester().assert_mongot_hosts_converged(
        _expected_mongot_hosts(mdbs, INITIAL_SHARD_COUNT, [_idx(mcc) for mcc in member_cluster_clients]),
        timeout=600,
    )

    _wait_for_state_cm_converged(
        CoreV1Api(api_client=central_cluster_client),
        namespace,
        {mcc.cluster_name: _idx(mcc) for mcc in member_cluster_clients},
        shard_names,
    )


# =============================================================================
# Data plane: $search via per-cluster mongos
# =============================================================================


@mark.e2e_search_mc_sharded_om_lifecycle
def test_restore_sample_database(namespace: str, tools_pod):
    tester = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_mc_sharded_om_lifecycle
def test_shard_sample_collection(namespace: str):
    """Shard sample_mflix.movies across the 3 shards so $search exercises every per-shard mongot."""
    admin = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    admin.shard_and_distribute_collection("sample_mflix", "movies")


@mark.e2e_search_mc_sharded_om_lifecycle
def test_create_search_index(namespace: str):
    tester = _per_cluster_mongos_search_tester(namespace, 0, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_per_cluster_search_query(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search via each cluster's mongos — proves every cluster's Envoy + per-shard mongot
    path returns results. Each cluster's mongos targets its OWN cluster's router proxy
    (baseline-asserted on-pod), so a per-cluster non-empty result validates that cluster's
    local (cluster x shard) data path.
    """
    for cluster_index in range(len(member_cluster_clients)):
        _wait_for_search_results(namespace, cluster_index)


# =============================================================================
# Lifecycle: shard removal, cluster-entry removal, and CR deletion
# =============================================================================


@mark.e2e_search_mc_sharded_om_lifecycle
def test_remove_shard_lifecycle(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
    mdbs: MongoDBSearch,
    member_cluster_clients: List[MultiClusterClient],
):
    """Dropping the trailing shard (search shard list FIRST so its mongot is torn down
    before the data-bearing shard disappears, then the source) must sweep the removed
    shard's mongot artifacts on every cluster, deregister exactly its OM hosts, prune the
    routing-ready latch, converge the forwarder state CM, and leave the live shards'
    artifacts untouched (UID-pinned) with every surviving pod still observing its own
    cluster's routing (the direct-AC keys sit outside spec, so the source reconcile's
    merge must preserve them)."""
    live_shards = _shard_names(REDUCED_SHARD_COUNT)
    removed_shard = _shard_names(INITIAL_SHARD_COUNT)[-1]

    live_uids: Dict[str, Dict[str, str]] = {}
    removed_readers: Dict[str, Dict[str, Callable[[], object]]] = {}
    for mcc in member_cluster_clients:
        live_uids[mcc.cluster_name] = _reader_uids(_local_artifact_readers(mcc, namespace, live_shards))
        readers = _shard_artifact_readers(mcc, namespace, removed_shard)
        # Presence guard: a 404 or missing-PVC failure here fails the capture, so the
        # deletion polls below can never pass vacuously.
        for read in readers.values():
            read()
        removed_sts = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, removed_shard, _idx(mcc))
        assert mongot_data_pvc_names(
            namespace, removed_sts, api_client=mcc.api_client
        ), f"[{mcc.cluster_name}] expected mongot data PVCs for {removed_sts}"
        removed_readers[mcc.cluster_name] = readers

    mdbs.load()
    mdbs["spec"]["source"]["external"]["shardedCluster"]["shards"] = _source_shard_list(namespace, REDUCED_SHARD_COUNT)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)

    mdb.load()
    mdb["spec"]["shardCount"] = REDUCED_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1800)

    _assert_routing_observed(namespace, mdb, member_cluster_clients, REDUCED_SHARD_COUNT)

    for mcc in member_cluster_clients:
        for what, read in removed_readers[mcc.cluster_name].items():
            wait_for_resource_deleted(read, f"{what} in {mcc.cluster_name}")
        wait_for_mongot_pvcs_deleted(
            namespace,
            search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, removed_shard, _idx(mcc)),
            api_client=mcc.api_client,
        )
        assert (
            _reader_uids(_local_artifact_readers(mcc, namespace, live_shards)) == live_uids[mcc.cluster_name]
        ), f"[{mcc.cluster_name}] live-shard artifact UIDs changed when shard {removed_shard} was removed"

    mdbs.load()
    mdb.get_om_tester().assert_mongot_hosts_converged(
        _expected_mongot_hosts(mdbs, REDUCED_SHARD_COUNT, [_idx(mcc) for mcc in member_cluster_clients]),
        timeout=600,
    )

    central_core = CoreV1Api(api_client=central_cluster_client)
    _wait_for_state_cm_converged(
        central_core,
        namespace,
        {mcc.cluster_name: _idx(mcc) for mcc in member_cluster_clients},
        live_shards,
    )

    def routing_latch_pruned() -> tuple:
        groups = _routing_ready_groups(central_core, namespace)
        return set(groups) == set(live_shards), f"routingReadyMongotGroups={sorted(groups)}, want {sorted(live_shards)}"

    run_periodically(
        routing_latch_pruned, timeout=300, sleep_time=5, msg="routing-ready latch to prune the removed shard"
    )

    _wait_for_search_results(namespace, 0, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_mc_sharded_om_lifecycle
def test_remove_cluster_entry(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
    mdbs: MongoDBSearch,
    member_cluster_clients: List[MultiClusterClient],
):
    """Dropping the cluster-3 entry must sweep every mongot/Envoy/forwarder artifact on
    that cluster (main + Envoy + metrics sweeps), deregister exactly its OM hosts, drop
    its entry from the topology-state CM, and leave BOTH survivors byte-identical
    (UID-pinned) and serving $search. The removed cluster's SOURCE processes keep running
    but point at its now-deleted proxies, so the customer rewires them (direct AC) to a
    survivor's proxies for the same shard before the data-plane asserts."""
    assert (
        len(member_cluster_clients) == CLUSTER_COUNT
    ), f"expected {CLUSTER_COUNT} member clusters, got {len(member_cluster_clients)}"
    survivors, removed = member_cluster_clients[:-1], member_cluster_clients[-1]
    assert (
        _idx(removed) == REMOVED_CLUSTER_INDEX
    ), f"expected the trailing entry to be index {REMOVED_CLUSTER_INDEX}, got {_idx(removed)}"

    live_shards = _shard_names(REDUCED_SHARD_COUNT)

    survivor_uids: Dict[str, Dict[str, str]] = {}
    survivor_forwarder_uids: Dict[str, Dict[str, str]] = {}
    for mcc in survivors:
        survivor_uids[mcc.cluster_name] = _reader_uids(_local_artifact_readers(mcc, namespace, live_shards))
        survivor_forwarder_uids[mcc.cluster_name] = _forwarder_artifact_uids(mcc, namespace, _idx(mcc))

    # Presence guard on the cluster being removed (mongot + Envoy + PVCs + forwarder).
    removed_readers = _local_artifact_readers(removed, namespace, live_shards)
    for read in removed_readers.values():
        read()
    for sts_name in _shard_sts_names(_idx(removed), live_shards):
        assert mongot_data_pvc_names(
            namespace, sts_name, api_client=removed.api_client
        ), f"[{removed.cluster_name}] expected mongot data PVCs for {sts_name}"
    _forwarder_artifact_uids(removed, namespace, _idx(removed))
    removed_protected_uids = _customer_input_uids(removed, namespace, _idx(removed))

    central_core = CoreV1Api(api_client=central_cluster_client)
    state_before = _state_cm_clusters(central_core, namespace)
    assert set(state_before) == {
        mcc.cluster_name for mcc in member_cluster_clients
    }, f"state CM clusters {sorted(state_before)} do not match the member clusters"

    mdbs.load()
    surviving_entries = [deepcopy(e) for e in mdbs["spec"]["clusters"] if e["name"] != removed.cluster_name]
    assert len(surviving_entries) == len(
        survivors
    ), f"expected one spec.clusters entry per survivor, got {surviving_entries}"
    mdbs["spec"]["clusters"] = surviving_entries
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)
    mdbs.assert_cluster_statuses(expected_count=len(survivors), expect_managed_lb=True, expect_metrics_forwarder=True)

    # The removed cluster's source processes now point at dead endpoints — the customer
    # repoints them at the REWIRE_TARGET_INDEX survivor's proxies (reachable via the mesh).
    _wire_routing_via_ac(
        mdb, namespace, [REMOVED_CLUSTER_INDEX], REDUCED_SHARD_COUNT, proxy_cluster_index=lambda _: REWIRE_TARGET_INDEX
    )

    _wait_for_cluster_artifacts_swept(removed, namespace, removed_readers, live_shards)
    _wait_for_forwarder_artifacts_deleted(removed, namespace, _idx(removed))

    mdbs.load()
    mdb.get_om_tester().assert_mongot_hosts_converged(
        _expected_mongot_hosts(mdbs, REDUCED_SHARD_COUNT, [_idx(mcc) for mcc in survivors]),
        timeout=600,
    )

    def state_entry_dropped() -> tuple:
        clusters = _state_cm_clusters(central_core, namespace)
        if removed.cluster_name in clusters:
            return False, f"state CM still tracks {removed.cluster_name}: {sorted(clusters)}"
        return True, f"state CM clusters: {sorted(clusters)}"

    run_periodically(state_entry_dropped, timeout=300, sleep_time=5, msg="metrics forwarder state entry cleanup")

    state_after = _state_cm_clusters(central_core, namespace)
    expected_survivor_state = {mcc.cluster_name: state_before[mcc.cluster_name] for mcc in survivors}
    assert state_after == expected_survivor_state, (
        f"survivor state CM entries changed when {removed.cluster_name} was removed: "
        f"before={expected_survivor_state}, after={state_after}"
    )

    # Effective routing after the removal: survivors keep their own-cluster endpoints;
    # the removed cluster's processes observe the rewired survivor endpoints.
    _assert_routing_observed(namespace, mdb, survivors, REDUCED_SHARD_COUNT)
    _assert_routing_observed(
        namespace, mdb, [removed], REDUCED_SHARD_COUNT, proxy_cluster_index=lambda _: REWIRE_TARGET_INDEX
    )

    for mcc in survivors:
        assert (
            _reader_uids(_local_artifact_readers(mcc, namespace, live_shards)) == survivor_uids[mcc.cluster_name]
        ), f"[{mcc.cluster_name}] managed artifact UIDs changed when {removed.cluster_name} was removed"
        assert (
            _forwarder_artifact_uids(mcc, namespace, _idx(mcc)) == survivor_forwarder_uids[mcc.cluster_name]
        ), f"[{mcc.cluster_name}] forwarder artifact UIDs changed when {removed.cluster_name} was removed"
        assert_workload_ready_in_cluster(
            mcc,
            namespace,
            {sts_name: MONGOT_REPLICAS_PER_CLUSTER for sts_name in _shard_sts_names(_idx(mcc), live_shards)},
            _forwarder_names(_idx(mcc))["deployment"],
            timeout=300,
        )
        _wait_for_search_results(namespace, _idx(mcc), timeout=SEARCH_INDEX_READY_TIMEOUT)

    assert (
        _customer_input_uids(removed, namespace, _idx(removed)) == removed_protected_uids
    ), f"[{removed.cluster_name}] customer-replicated Search inputs changed during the cluster sweep"


@mark.e2e_search_mc_sharded_om_lifecycle
def test_delete_search_resource(
    namespace: str,
    mdb: MongoDB,
    mdbs: MongoDBSearch,
    member_cluster_clients: List[MultiClusterClient],
):
    """CR delete: OM must drop every MONGOT host before the metrics finalizer releases
    (the CR reading back 404 proves the whole pre-deletion path ran), the label sweeps
    must empty every member cluster — including already-removed cluster-3 — the
    customer-replicated inputs must survive untouched, and unwiring the search
    setParameters from the still-Running source must drain them out of the automation
    config. Destroys the workload — keep it last."""
    survivors, removed = member_cluster_clients[:-1], member_cluster_clients[-1]
    live_shards = _shard_names(REDUCED_SHARD_COUNT)

    per_cluster = []
    for mcc in survivors:
        readers = _local_artifact_readers(mcc, namespace, live_shards)
        for read in readers.values():
            read()
        for sts_name in _shard_sts_names(_idx(mcc), live_shards):
            assert mongot_data_pvc_names(
                namespace, sts_name, api_client=mcc.api_client
            ), f"[{mcc.cluster_name}] expected mongot data PVCs for {sts_name}"
        per_cluster.append((mcc, readers))
    protected_uids = {
        mcc.cluster_name: _customer_input_uids(mcc, namespace, _idx(mcc)) for mcc in member_cluster_clients
    }

    om_tester = mdb.get_om_tester()
    mdbs.delete()

    # Phase A: the exact-empty MONGOT host set proves deregistration ran before the
    # finalizer released (the source's own MONGOD/MONGOS hosts are unaffected).
    om_tester.assert_mongot_hosts_converged(set(), timeout=600)
    for mcc in survivors:
        _wait_for_forwarder_artifacts_deleted(mcc, namespace, _idx(mcc))
    wait_for_search_deleted(mdbs, timeout=600)

    for mcc, readers in per_cluster:
        _wait_for_cluster_artifacts_swept(mcc, namespace, readers, live_shards)
    # Cluster-3 was swept on entry removal and must stay empty through the CR delete.
    wait_for_search_owned_resources_deleted(
        removed.apps_v1_api(),
        removed.core_v1_api(),
        namespace,
        MDBS_RESOURCE_NAME,
        where=removed.cluster_name,
    )

    for mcc in member_cluster_clients:
        assert (
            _customer_input_uids(mcc, namespace, _idx(mcc)) == protected_uids[mcc.cluster_name]
        ), f"[{mcc.cluster_name}] customer-replicated Search inputs changed during CR deletion"

    # The search wiring on an external source is customer-owned, so the operator cannot
    # drop it on Search delete the way it does for internal sources: the customer removes
    # the direct-AC routing keys per process, then clears the base keys from the spec so
    # the operator's key-removal diff drains them. The poll below proves all six keys
    # leave every process's automation config while the source stays Running.
    remove_mongot_host_via_ac(mdb)
    mdb.load()
    cleared = {key: None for key in BASE_SEARCH_SET_PARAMETER}
    mdb["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": cleared}
    mdb["spec"]["mongos"]["additionalMongodConfig"] = {"setParameter": cleared}
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def search_parameters_dropped() -> tuple:
        ac_tester = om_tester.get_automation_config_tester()
        leftovers = []
        for process in ac_tester.get_all_processes():
            set_parameter = process.get("args2_6", {}).get("setParameter", {})
            present = [key for key in SEARCH_SET_PARAMETER_KEYS if key in set_parameter]
            if present:
                leftovers.append(f"{process['name']}: {present}")
        return not leftovers, f"search setParameters still in automation config: {leftovers}"

    run_periodically(
        search_parameters_dropped,
        timeout=300,
        sleep_time=10,
        msg="source automation config to drop the search setParameters",
    )
