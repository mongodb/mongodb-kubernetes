"""Simulated multi-cluster MongoDBSearch e2e with a SHARDED external source.

Topology: 1 central operator + 2 simulated-MC search operators (cluster-1/cluster-2,
harness indexes 0/1). The sharded source is PINNED to cluster-3 (harness index 2),
which runs NO search operator.

Why pin: a member that serves the MongoDB CRD reaps the source's StatefulSets as
orphans because their cross-cluster MongoDB ownerRef is unresolvable there. Parking
the source on an operator-free cluster keeps that dangling ownerRef inert. The search
clusters run operators too but hold no MongoDB-owned resources, so they have nothing
to reap.

With exactly one referenced cluster the source's internal index is 0 (pods
{mdb}-{shard}-0-{pod} / {mdb}-mongos-0-{pod}) even though it schedules on
member_cluster_clients[2]; the search side keeps harness indexes 0/1, so the two
index systems never collide.
"""

from __future__ import annotations

import os
from typing import Dict, List, Tuple

import kubernetes
import pymongo.errors
import pytest
from kubetester import try_load
from kubetester.certs import create_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod
from tests.common.multicluster.multicluster_utils import assert_workload_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.mc_search_helper import (
    assert_mongot_sync_source_hosts,
    patch_mongot_host_via_ac,
    read_mongod_set_parameter,
    replicate_sharded_search_secrets_to_members,
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
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    per_cluster_search_tester,
    verify_search_results_from_all_shards,
)
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
    _idx,
    apply_search_crs_and_assert_running,
    assert_cross_cluster_isolation,
    assert_per_cluster_count,
    assert_status_running_local_only,
    build_per_cluster_search_crs,
    create_search_users,
    install_central_mc_operator,
    install_simulated_operators_per_member,
)

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-sim-sh"
MDBS_RESOURCE_NAME = "mdb-mc-sim-sh-search"

# Source sharded MongoDB pinned to one cluster => source-internal index 0 (see module
# docstring), so every per-cluster distribution below is single-element with a -0- suffix.
SHARD_COUNT = 2
MEMBERS_PER_CLUSTER: List[int | None] = [1]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [1]
MONGOS_PER_CLUSTER: List[int | None] = [1]

# Harness member index of the source cluster (cluster-3); it runs no search operator.
SOURCE_MEMBER_INDEX = 2
SOURCE_CLUSTER_IDX = 0

MONGOT_REPLICAS_PER_CLUSTER = 1

# create_issuer_ca writes a same-named ConfigMap (source MongoDB TLS spec; keys ca-pem, mms-ca.crt)
# and Secret (MongoDBSearch spec.source.external.tls.ca; key ca.crt).
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 90

# Search setParameters the operator must apply to every source mongod/mongos so the
# mongod->Envoy->mongot hop is mTLS+gRPC. Asserted back at runtime, not just set in spec.
SOURCE_SEARCH_SET_PARAMETERS = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "requireTLS",
    "useGrpcForSearch": True,
}


def _source_mongos_search_tester(
    namespace: str,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to the single source mongos via its per-pod headless Service ({mdb}-mongos-0-0-svc) — reachable cross-mesh from any member."""
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{SOURCE_CLUSTER_IDX}-0-svc.{namespace}.svc.cluster.local:27017"
    return per_cluster_search_tester(mongos_host, username, password)


def _shard_host_seeds(namespace: str, shard_idx: int) -> List[str]:
    """A shard's source-mongod seed hosts (what its mongot syncs from); mirrors the per-shard
    `hosts` in per_cluster_mdbs_search (trailing -0 = source-internal index 0)."""
    return [
        f"{MDB_RESOURCE_NAME}-{shard_idx}-{cluster_idx}-0-svc.{namespace}.svc.cluster.local:27017"
        for cluster_idx, n_members in enumerate(MEMBERS_PER_CLUSTER)
        if n_members
    ]


@fixture(scope="module")
def central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: dict,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    """Central MC operator managing the source sharded MongoDB (all three members, watch_search=True)."""
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
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """Issuer CA ConfigMap written to central + ALL members: the source cluster (cluster-3)
    verifies its own TLS material against it, and the search clusters' mongots verify the
    source's TLS during sync. The operator consumes the CA as a ConfigMap, never a Secret.
    """
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
        logger.info(f"CA ConfigMap {CA_CONFIGMAP_NAME} created in cluster {mcc.cluster_name}")
    return name


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDB:
    """MC sharded MongoDB source (TLS+SCRAM) pinned to cluster-3 only; managed by the central operator."""
    source_cluster_names = [member_cluster_names[SOURCE_MEMBER_INDEX]]
    resource = MongoDB.from_yaml(
        yaml_fixture("search-q3-mc-sharded.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["shardCount"] = SHARD_COUNT

    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, MEMBERS_PER_CLUSTER)
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(source_cluster_names, MONGOS_PER_CLUSTER)

    # mongotHost/searchIndexManagementHostAndPort are intentionally absent from the CR
    # spec: routing is set later directly on the OM Automation Config (the flip test),
    # so operator reconciles never clobber the patch.
    resource["spec"]["mongos"]["additionalMongodConfig"] = {"setParameter": dict(SOURCE_SEARCH_SET_PARAMETERS)}
    resource["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": dict(SOURCE_SEARCH_SET_PARAMETERS)}

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }

    # Requests-only (no CPU limits): limits throttle the source's CPU-hungry TLS/replset
    # startup, widening the window where mongot races it. Don't override startupProbe — a
    # partial override drops the operator's exec handler and the apiserver rejects it.
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


@fixture(scope="module")
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR applied to each member's API; each operator's LocalizeToCluster collapse fans out its own per-(cluster, shard) mongot resources."""
    member_cluster_clients = member_cluster_clients[:2]

    # Source pinned to one cluster => cluster_idx is 0 here (distinct from search 0/1);
    # per-pod headless Services are cross-cluster-reachable from cluster-1/2 via Istio.
    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{cluster_idx}-{pod_idx}-svc.{namespace}.svc.cluster.local:27017"
        for cluster_idx, n_mongos in enumerate(MONGOS_PER_CLUSTER)
        if n_mongos
        for pod_idx in range(n_mongos)
    ]
    shards = [
        {
            "shardName": f"{MDB_RESOURCE_NAME}-{shard_idx}",
            "hosts": [
                f"{MDB_RESOURCE_NAME}-{shard_idx}-{cluster_idx}-0-svc.{namespace}.svc.cluster.local:27017"
                for cluster_idx, n_members in enumerate(MEMBERS_PER_CLUSTER)
                if n_members
            ],
        }
        for shard_idx in range(SHARD_COUNT)
    ]

    return build_per_cluster_search_crs(
        namespace,
        member_cluster_clients,
        MDBS_RESOURCE_NAME,
        MONGOT_REPLICAS_PER_CLUSTER,
        # Per-cluster literal with the clusterIndex baked in (distinct per cluster); the
        # {shardName} placeholder is retained for the operator to resolve per shard,
        # mirroring q3_mc_sharded_external_mtls.
        lambda idx: search_resource_names.shard_proxy_svc_hostname_template(MDBS_RESOURCE_NAME, namespace, idx),
        {
            "shardedCluster": {
                "router": {"hosts": router_hosts},
                "shards": shards,
            },
            "tls": {"ca": {"name": ca_configmap}},
        },
        # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc FQDN
        # (distinct per cluster via the cluster index).
        router_hostname_for_cluster=lambda idx: search_resource_names.mc_proxy_svc_fqdn(
            MDBS_RESOURCE_NAME, namespace, idx
        ),
    )


@mark.e2e_search_simulated_mc_sharded
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.wait_for_operator_ready()


@mark.e2e_search_simulated_mc_sharded
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
):
    """Source MongoDB per-component TLS certs (q3 pattern); central MongoDB controller replicates them to members."""

    def _issue(component_resource: str, secret_name: str, distribution: List[int | None]):
        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=component_resource,
            replicas_cluster_distribution=distribution,
            secret_name=secret_name,
            api_client=central_cluster_client,
        )

    for shard_idx in range(SHARD_COUNT):
        _issue(
            component_resource=f"{MDB_RESOURCE_NAME}-{shard_idx}",
            secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-{shard_idx}-cert",
            distribution=MEMBERS_PER_CLUSTER,
        )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-config",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-config-cert",
        distribution=CONFIG_SRV_PER_CLUSTER,
    )
    _issue(
        component_resource=f"{MDB_RESOURCE_NAME}-mongos",
        secret_name=f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-mongos-cert",
        distribution=MONGOS_PER_CLUSTER,
    )
    logger.info(f"Source sharded cluster per-component TLS certs created (prefix={SOURCE_CERT_PREFIX})")


@mark.e2e_search_simulated_mc_sharded
def test_mongodb_running(mdb: MongoDB):
    mdb.update()
    # ignore_errors=True: tolerate transient phase=Failed from controller-runtime cache lag on MC STS Create.
    mdb.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)


@mark.e2e_search_simulated_mc_sharded
def test_source_pinned_to_cluster_3(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Guard the two facts every downstream FQDN depends on: the source carries internal
    index 0 (shard STS {mdb}-0-0 exists) AND physically lives on cluster-3 (harness index 2).
    """
    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]
    assert _idx(source_mcc) == SOURCE_MEMBER_INDEX, (
        f"expected harness member index {SOURCE_MEMBER_INDEX} for the source cluster, "
        f"got {_idx(source_mcc)} ({source_mcc.cluster_name})"
    )
    for shard_idx in range(SHARD_COUNT):
        # MC sharded STS = {mdb}-{shardIdx}-{clusterIdx} (MultiShardRsName); pods append -{pod}.
        sts_name = f"{MDB_RESOURCE_NAME}-{shard_idx}-{SOURCE_CLUSTER_IDX}"
        sts = source_mcc.read_namespaced_stateful_set(sts_name, namespace)
        assert (sts.status.ready_replicas or 0) >= 1, f"[{source_mcc.cluster_name}] {sts_name} not ready"
        logger.info(f"[{source_mcc.cluster_name}] source shard STS {sts_name} present and ready")


@mark.e2e_search_simulated_mc_sharded
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    """Create users before sim-ops install: AC v8 must propagate to mongod agents while the cluster is undisturbed."""
    create_search_users(namespace, central_cluster_client, admin_user, mdb_user, mongot_user)


@mark.e2e_search_simulated_mc_sharded
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    """Install sim ops after MongoDB Running + users Updated: sim-op install perturbs member-cluster state and races central-op reconcile + AC propagation."""
    install_simulated_operators_per_member(
        namespace, multi_cluster_operator_installation_config, member_cluster_clients
    )


@mark.e2e_search_simulated_mc_sharded
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Per-cluster, per-shard LB certs issued on central (SANs cover per-shard + cluster-level proxy FQDNs)."""
    member_cluster_clients = member_cluster_clients[:2]
    create_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        shard_count=SHARD_COUNT,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        cluster_indexes=[_idx(mcc) for mcc in member_cluster_clients],
        api_client=central_cluster_client,
    )


@mark.e2e_search_simulated_mc_sharded
def test_create_search_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Per-(cluster, shard) mongot TLS certs issued on central (cert-manager runs there only); next step copies Secrets to members."""
    member_cluster_clients = member_cluster_clients[:2]
    for mcc in member_cluster_clients:
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
        logger.info(f"Per-shard Search TLS certs created on central for cluster_index={_idx(mcc)}")


@mark.e2e_search_simulated_mc_sharded
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material must be present locally."""
    replicate_sharded_search_secrets_to_members(
        namespace=namespace,
        central_cluster_client=central_cluster_client,
        member_cluster_clients=member_cluster_clients[:2],
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mdbs_tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        shard_count=SHARD_COUNT,
        mongot_user_name=MONGOT_USER_NAME,
    )


@mark.e2e_search_simulated_mc_sharded
def test_search_cr_reaches_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Same CR applied to each member; each simulated operator stamps Running on its own slice."""
    apply_search_crs_and_assert_running(per_cluster_mdbs_search)


@mark.e2e_search_simulated_mc_sharded
def test_per_cluster_sharded_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each member materialises its own (cluster, shard) mongot STS/Svc/CM/proxy, the
    cluster-level mongos-target proxy svc, and a per-cluster Envoy LB.
    """
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
                f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, "
                f"got {sts.spec.replicas}"
            )
            mcc.read_namespaced_service(svc_name, namespace)
            mcc.read_namespaced_service(proxy_svc_name, namespace)
            sts_ready[sts_name] = MONGOT_REPLICAS_PER_CLUSTER
            local_mongot_upstreams.append(f"{svc_name}.{namespace}.svc.cluster.local")
            assert_mongot_sync_source_hosts(mcc, cm_name, namespace, _shard_host_seeds(namespace, shard_idx))
            logger.info(
                f"[{mcc.cluster_name}] (cluster_index={ci}, shard={shard_name}): "
                f"sts={sts_name} svc={svc_name} proxy={proxy_svc_name} cm={cm_name}"
            )
        upstreams_by_idx[ci] = local_mongot_upstreams

        # Cluster-level mongos-target proxy svc is per-cluster (not per-shard).
        mcc.read_namespaced_service(search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, ci), namespace)

        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        assert_workload_ready_in_cluster(mcc, namespace, sts_ready, envoy_dep_name)

        mdbs.assert_lb_status()
        logger.info(f"[{mcc.cluster_name}] envoy_lb={envoy_dep_name} ready; mongot STSs ready: {list(sts_ready)}")

    verify_per_cluster_envoy_sni(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients=[mcc for mcc, _ in per_cluster_mdbs_search],
        expected_upstreams_by_idx=upstreams_by_idx,
    )


@mark.e2e_search_simulated_mc_sharded
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_status_running_local_only(per_cluster_mdbs_search)


# The source is a single sharded deployment, so there are NO per-cluster source
# processes to route locally. Instead we FLIP the source's mongotHost to each search
# cluster's Envoy in turn, proving each cluster's mongot independently indexes the
# shared source and serves correct rows.


def _mongos_envoy_endpoint(namespace: str, cluster_idx: int) -> str:
    """A search cluster's cluster-level Envoy proxy-svc FQDN:port (the mongos mongot target)."""
    return f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, cluster_idx)}:{ENVOY_PROXY_PORT}"


def _shard_envoy_endpoint(namespace: str, shard_idx: int, cluster_idx: int) -> str:
    """A search cluster's per-shard Envoy proxy-svc FQDN:port (the shard mongod mongot target)."""
    shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
    return search_resource_names.shard_proxy_service_host(
        MDBS_RESOURCE_NAME, shard_name, namespace, ENVOY_PROXY_PORT, cluster_idx
    )


def _flip_source_mongot_host(namespace: str, mdb: MongoDB, target_idx: int) -> None:
    """Point every source process at the target cluster's Envoy via OM AC: mongos -> cluster-level
    Envoy, config servers untouched. parts[0] (after the {mdb}- prefix) is the shard index for
    shard mongods (parses as int) -> per-shard Envoy; mongos/config don't parse as int.
    """
    mongos_endpoint = _mongos_envoy_endpoint(namespace, target_idx)
    prefix = f"{MDB_RESOURCE_NAME}-"

    def resolve_host(process_name: str) -> str | None:
        if not process_name.startswith(prefix):
            return None
        parts = process_name[len(prefix) :].split("-")
        if parts[0] == "mongos":
            return mongos_endpoint
        if parts[0] == "config":
            return None
        try:
            shard_idx = int(parts[0])
        except ValueError:
            return None
        return _shard_envoy_endpoint(namespace, shard_idx, target_idx)

    patch_mongot_host_via_ac(mdb, resolve_host)


def _search_tls_param_mismatches(params: dict) -> List[str]:
    # automation-mongod.conf serializes bool setParameters as the strings "true"/"false",
    # while mongos getParameter returns real bools; coerce both sides so a correctly-applied
    # value never reads as a mismatch, without masking a genuinely wrong one.
    return [
        f"{k}={params.get(k)!r} expected {v!r}"
        for k, v in SOURCE_SEARCH_SET_PARAMETERS.items()
        if str(params.get(k)).lower() != str(v).lower()
    ]


def _assert_source_routed_to(namespace: str, source_mcc: MultiClusterClient, target_idx: int) -> None:
    """Positively verify the new mongotHost is APPLIED on the source before querying:
    each shard mongod's on-disk automation-mongod.conf shows the target cluster's
    per-shard Envoy, and the (stateless) mongos reports the target's cluster-level
    Envoy via getParameter.
    """

    def shards_on_disk() -> tuple:
        all_ok = True
        msgs: List[str] = []
        for shard_idx in range(SHARD_COUNT):
            pod_name = f"{MDB_RESOURCE_NAME}-{shard_idx}-{SOURCE_CLUSTER_IDX}-0"
            expected = _shard_envoy_endpoint(namespace, shard_idx, target_idx)
            try:
                params = read_mongod_set_parameter(pod_name, namespace, source_mcc.api_client)
                got_host = params.get("mongotHost", "")
                got_mgmt = params.get("searchIndexManagementHostAndPort", "")
                tls_bad = _search_tls_param_mismatches(params)
                if got_host != expected or got_mgmt != expected or tls_bad:
                    all_ok = False
                    msgs.append(
                        f"[{source_mcc.cluster_name}] {pod_name}: mongotHost={got_host!r} "
                        f"searchIndexMgmt={got_mgmt!r} expected={expected!r} tls_params_bad={tls_bad}"
                    )
                else:
                    msgs.append(f"[{source_mcc.cluster_name}] {pod_name}: mongotHost={expected} + search TLS params OK")
            except Exception as exc:
                all_ok = False
                msgs.append(f"[{source_mcc.cluster_name}] {pod_name}: error reading conf: {exc}")
        return all_ok, "\n".join(msgs)

    run_periodically(
        shards_on_disk, timeout=300, sleep_time=10, msg=f"source shard mongotHost -> cluster idx {target_idx}"
    )

    mongos_expected = _mongos_envoy_endpoint(namespace, target_idx)
    tester = _source_mongos_search_tester(namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    get_params = {"getParameter": 1, "mongotHost": 1, "searchIndexManagementHostAndPort": 1}
    get_params.update({k: 1 for k in SOURCE_SEARCH_SET_PARAMETERS})
    params = tester.client.admin.command(get_params)
    got_host = params.get("mongotHost", "")
    got_mgmt = params.get("searchIndexManagementHostAndPort", "")
    tls_bad = _search_tls_param_mismatches(params)
    assert got_host == mongos_expected and got_mgmt == mongos_expected and not tls_bad, (
        f"[{source_mcc.cluster_name}] mongos mongotHost={got_host!r} searchIndexMgmt={got_mgmt!r}, "
        f"expected {mongos_expected!r}; search TLS param mismatches={tls_bad}"
    )
    logger.info(f"source routed to cluster idx {target_idx}: mongos+shards mongotHost + search TLS params confirmed")


@mark.e2e_search_simulated_mc_sharded
def test_shard_sample_collection(namespace: str):
    """Hash-shard the EMPTY sample_mflix.movies before loading data, so hashed _id
    distributes docs across both shards at insert time. create_index implicitly creates
    the empty collection. Skip reshardCollection (it hangs in 3-operator simulated-MC).
    """
    admin = _source_mongos_search_tester(namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
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


@mark.e2e_search_simulated_mc_sharded
def test_restore_sample_database(namespace: str, tools_pod: ToolsPod):
    """Restore sample_mflix via the source mongos WITHOUT --drop: the movies collection is
    already hash-sharded (empty), so inserts distribute across both shards; --drop would
    un-shard it.
    """
    tester = _source_mongos_search_tester(namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
        drop=False,
    )


@mark.e2e_search_simulated_mc_sharded
def test_shard_distribution(namespace: str):
    """Both shards must hold data — proves the pre-shard-then-load distributed inserts."""
    tester = _source_mongos_search_tester(namespace, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def check() -> tuple:
        counts = movies.get_shard_document_counts()
        total = sum(counts.values())
        if len(counts) != SHARD_COUNT or not all(c > 0 for c in counts.values()):
            return False, f"not all {SHARD_COUNT} shards non-empty: counts={counts} total={total}"
        # Require each shard to hold at least 10% of docs — all>0 would pass even with 99% skew.
        if min(counts.values()) <= total * 0.10:
            return (
                False,
                f"shard balance floor not met: min={min(counts.values())} <= 10% of total={total}; counts={counts}",
            )
        return True, f"per-shard counts: {counts} total={total}"

    run_periodically(check, timeout=120, sleep_time=5, msg="per-shard document distribution")


def _route_and_query(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    target_idx: int,
    *,
    create_index: bool,
) -> None:
    """Flip the source at the target cluster's mongot, verify it APPLIED, then assert the
    deterministic $search count via the source mongos. create_index=True (first flip)
    builds the index (needs searchIndexManagementHostAndPort, which only the flip sets);
    later flips reuse it and just wait for the target mongot to report READY, so a
    propagation failure surfaces as a clear timeout rather than an empty $search.
    """
    search_clients = member_cluster_clients[:2]
    assert_per_cluster_count(search_clients)
    assert target_idx in {_idx(mcc) for mcc in search_clients}, f"search cluster idx {target_idx} not found"
    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]

    _flip_source_mongot_host(namespace, mdb, target_idx)
    _assert_source_routed_to(namespace, source_mcc, target_idx)

    tester = _source_mongos_search_tester(namespace, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    if create_index:
        movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)
    # Deterministic compound query: the Hawaii/Alaska sample returns exactly 4 docs.
    movies.assert_search_query(retry_timeout=SEARCH_QUERY_RETRY_TIMEOUT)
    logger.info(f"search cluster idx {target_idx}: deterministic $search (==4) via source mongos OK")


@mark.e2e_search_simulated_mc_sharded
def test_route_to_search_cluster_1_and_query(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    """Flip 1 of 2: route the source at cluster-1's (idx0) mongot and create the index."""
    _route_and_query(namespace, mdb, member_cluster_clients, 0, create_index=True)


@mark.e2e_search_simulated_mc_sharded
def test_search_results_from_all_shards(namespace: str):
    """Cross-shard completeness (still routed at cluster-1 from flip 1): a wildcard
    $search via the source mongos returns total-1 docs, proving EVERY shard's mongot
    contributes — not merely that data landed on both shards.
    """
    tester = _source_mongos_search_tester(namespace, USER_NAME, USER_PASSWORD)
    verify_search_results_from_all_shards(tester)


@mark.e2e_search_simulated_mc_sharded
def test_create_vector_search_index(namespace: str):
    """Create a $vectorSearch index on embedded_movies and wait for READY (still routed at
    cluster-1). Indexing needs no Voyage key; the query is env-gated, so only create+READY is asserted.
    """
    tester = _source_mongos_search_tester(namespace, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.create_vector_search_index()
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_sharded
def test_vector_search_query_via_source_mongos(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """$vectorSearch via the source mongos in the cluster-1 window — proves the vector data
    path serves rows, not just that the index reaches READY. Re-verifies the route is APPLIED
    on the source then re-waits READY so a propagation regression surfaces as a timeout.
    embedded_movies is never sharded, so it lives on the primary shard and limit=4 against
    thousands of pre-embedded docs returns exactly 4 (count==limit invariant of ordering).
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")

    source_mcc = member_cluster_clients[SOURCE_MEMBER_INDEX]
    _assert_source_routed_to(namespace, source_mcc, 0)

    limit = 4
    tester = _source_mongos_search_tester(namespace, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)

    query_vector = embedded.generate_query_vector("space exploration")
    doc_count = embedded.count_documents_with_embeddings()
    assert doc_count >= limit, (
        f"only {doc_count} embedded_movies docs have plot_embedding_voyage_3_large — "
        f"need at least {limit} for a deterministic count==limit assertion"
    )

    embedded.assert_vector_search_count_eventually(
        query_vector,
        limit,
        timeout=SEARCH_QUERY_RETRY_TIMEOUT,
        msg_prefix="via source mongos: ",
    )
    logger.info(f"deterministic $vectorSearch (=={limit}) via source mongos OK")


@mark.e2e_search_simulated_mc_sharded
def test_route_to_search_cluster_2_and_query(
    namespace: str,
    mdb: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    """Flip 2 of 2: re-point at cluster-2's (idx1) mongot, proving it independently
    indexes + serves the shared source (index created once under flip 1).
    """
    _route_and_query(namespace, mdb, member_cluster_clients, 1, create_index=False)


@mark.e2e_search_simulated_mc_sharded
def test_cross_cluster_isolation_absence(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each cluster must NOT contain the OTHER cluster's per-(cluster,shard) Search resources.
    Confirms each simulated operator only materialises its own LocalizeToCluster slice.
    """
    shard_names = [f"{MDB_RESOURCE_NAME}-{shard_idx}" for shard_idx in range(SHARD_COUNT)]
    assert_cross_cluster_isolation(namespace, per_cluster_mdbs_search, MDBS_RESOURCE_NAME, shard_names=shard_names)
