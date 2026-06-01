"""Simulated multi-cluster MongoDBSearch e2e with a SHARDED external source.

Topology mirrors simulated_mc_basic.py (1 central + 2 simulated-MC operators) but
the source is a topology: MultiCluster sharded MongoDB. See sibling for shared shape.
"""

from __future__ import annotations

import os
from typing import List, Tuple

import kubernetes
import pymongo.errors
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
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod
from tests.common.search import search_resource_names
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
    ENVOY_LB_REPLICAS,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    MONGOT_USER_PASSWORD,
    SOURCE_CERT_PREFIX,
    USER_NAME,
    USER_PASSWORD,
    _build_user,
    _idx,
    _install_simulated_operator,
    _replicate_mongot_user_password_to_members,
    assert_foreign_resources_absent,
    assert_per_cluster_count,
    read_state_configmap_mapping,
)

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MDB_RESOURCE_NAME = "mdb-mc-sim-sh"
MDBS_RESOURCE_NAME = "mdb-mc-sim-sh-search"

# Multi-cluster sharded source: 2 shards, 1 mongod-per-shard-per-cluster,
# 1 configsrv-per-cluster, 1 mongos-per-cluster. Two member clusters.
SHARD_COUNT = 2
MEMBERS_PER_CLUSTER: List[int | None] = [1, 1]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [1, 1]
MONGOS_PER_CLUSTER: List[int | None] = [1, 1]

MONGOT_REPLICAS_PER_CLUSTER = 1

# --- TLS-everywhere constants ----------------------------------------------
# CA ConfigMap (key: ca-pem / mms-ca.crt) consumed by the source MongoDB TLS spec
# and CA Secret (key: ca.crt) consumed by spec.source.external.tls.ca on the
# MongoDBSearch CR. create_issuer_ca writes both with the same name.
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 90


def _per_cluster_mongos_search_tester(
    namespace: str,
    cluster_index: int,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to one cluster's mongos via its per-pod headless Service + directConnection=true."""
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{mongos_host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


# ---------------------------------------------------------------------------
# Fixtures
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
    """Central MC op watches MongoDB+User+MultiCluster+mongodbsearch; the field-indexer for MongoDBSearch is only registered when mongodbsearch is in watchedResources."""
    from tests.conftest import (
        MULTI_CLUSTER_OPERATOR_NAME,
        _install_multi_cluster_operator,
        local_operator,
        run_kube_config_creation_tool,
    )

    member_cluster_clients = member_cluster_clients[:2]
    member_cluster_names = member_cluster_names[:2]

    os.environ["HELM_KUBECONTEXT"] = central_cluster_name

    if not local_operator():
        run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)

    return _install_multi_cluster_operator(
        namespace,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients,
        {
            "operator.name": MULTI_CLUSTER_OPERATOR_NAME,
            "operator.createOperatorServiceAccount": "false",
            "operator.watchedResources": "{mongodb,opsmanagers,mongodbusers,mongodbcommunity,mongodbmulticluster,mongodbsearch}",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    """Issuer CA written to central + every member (members need the Secret half for mongot TLS verify)."""
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients[:2]:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
        logger.info(f"CA ConfigMap/Secret {CA_CONFIGMAP_NAME} created in cluster {mcc.cluster_name}")
    return name


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDB:
    """MC sharded MongoDB source with TLS+SCRAM. Reuses q3 fixture; overrides name + shardCount."""
    member_cluster_names = member_cluster_names[:2]
    resource = MongoDB.from_yaml(
        yaml_fixture("search-q3-mc-sharded.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["shardCount"] = SHARD_COUNT

    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MONGOS_PER_CLUSTER)

    # MC MongoDB shares setParameter across clusters; mongot lookups funnel through cluster-0's Envoy (Istio east-west).
    cluster_idx = 0
    cluster_level_endpoint = (
        f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, cluster_idx)}:{ENVOY_PROXY_PORT}"
    )

    def _shard_proxy_endpoint(shard_name: str) -> str:
        proxy_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, cluster_idx)
        return f"{proxy_name}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"

    base_search_set_parameter = {
        "skipAuthenticationToSearchIndexManagementServer": False,
        "skipAuthenticationToMongot": False,
        "searchTLSMode": "requireTLS",
        "useGrpcForSearch": True,
    }

    resource["spec"]["shardOverrides"] = [
        {
            "shardNames": [f"{MDB_RESOURCE_NAME}-{shard_idx}"],
            "additionalMongodConfig": {
                "setParameter": {
                    **base_search_set_parameter,
                    "mongotHost": _shard_proxy_endpoint(f"{MDB_RESOURCE_NAME}-{shard_idx}"),
                    "searchIndexManagementHostAndPort": _shard_proxy_endpoint(f"{MDB_RESOURCE_NAME}-{shard_idx}"),
                },
            },
        }
        for shard_idx in range(SHARD_COUNT)
    ]

    resource["spec"]["mongos"]["additionalMongodConfig"] = {
        "setParameter": {
            **base_search_set_parameter,
            "mongotHost": cluster_level_endpoint,
            "searchIndexManagementHostAndPort": cluster_level_endpoint,
        },
    }
    resource["spec"]["shard"]["additionalMongodConfig"] = {
        "setParameter": base_search_set_parameter,
    }

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-admin.yaml",
        ADMIN_USER_NAME,
        ADMIN_USER_NAME,
        namespace,
        central_cluster_client,
        MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def mdb_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-user.yaml",
        USER_NAME,
        USER_NAME,
        namespace,
        central_cluster_client,
        MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
        MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def tools_pod(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> ToolsPod:
    """Pod placed on a member cluster so its DNS resolves the local mongos pod-FQDN."""
    return mongodb_tools_pod.get_tools_pod(namespace, api_client=member_cluster_clients[0].api_client)


@fixture(scope="module")
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR applied to each member's API; each operator's LocalizeToCluster collapse fans out its own per-(cluster, shard) mongot resources."""
    member_cluster_clients = member_cluster_clients[:2]
    results: List[Tuple[MultiClusterClient, MongoDBSearch]] = []

    clusters_spec = [
        {
            "clusterName": mcc.cluster_name,
            "clusterIndex": _idx(mcc),
            "replicas": MONGOT_REPLICAS_PER_CLUSTER,
        }
        for mcc in member_cluster_clients
    ]

    # router.hosts = every cluster's mongos pod-FQDN; shards[i].hosts = every cluster's mongod pod-FQDN (pod-svcs are cross-cluster-reachable via Istio).
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

    for mcc in member_cluster_clients:
        mdbs = MongoDBSearch.from_yaml(
            yaml_fixture("simulated-mc-search.yaml"),
            name=MDBS_RESOURCE_NAME,
            namespace=namespace,
        )
        mdbs["spec"]["clusters"] = clusters_spec
        mdbs["spec"]["loadBalancer"] = {
            "managed": {
                "replicas": ENVOY_LB_REPLICAS,
                # Per-cluster, per-shard externalHostname template — operator
                # substitutes {clusterIndex} and {shardName} per resource.
                "externalHostname": (
                    f"{MDBS_RESOURCE_NAME}-search-{{clusterIndex}}-{{shardName}}-proxy-svc"
                    f".{namespace}.svc.cluster.local"
                ),
            },
        }
        # Replace the fixture's hostAndPorts default with shardedCluster source.
        mdbs["spec"]["source"] = {
            "username": MONGOT_USER_NAME,
            "passwordSecretRef": {
                "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
                "key": "password",
            },
            "external": {
                "shardedCluster": {
                    "router": {"hosts": router_hosts},
                    "shards": shards,
                },
                "tls": {"ca": {"name": ca_configmap}},
            },
        }
        mdbs["spec"]["security"] = {
            "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
        }
        mdbs.api = kubernetes.client.CustomObjectsApi(mcc.api_client)
        try_load(mdbs)
        results.append((mcc, mdbs))

    return results


# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.assert_is_running()


@mark.e2e_search_simulated_mc_sharded_basic
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


@mark.e2e_search_simulated_mc_sharded_basic
def test_mongodb_running(mdb: MongoDB):
    mdb.update()
    # ignore_errors=True: tolerate transient phase=Failed from controller-runtime cache lag on MC STS Create.
    mdb.assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)


@mark.e2e_search_simulated_mc_sharded_basic
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
    member_cluster_clients: List[MultiClusterClient],
):
    """Create users before sim-ops install: AC v8 must propagate to mongod agents while the cluster is undisturbed."""
    member_cluster_clients = member_cluster_clients[:2]
    for user, pwd in (
        (admin_user, ADMIN_USER_PASSWORD),
        (mdb_user, USER_PASSWORD),
        (mongot_user, MONGOT_USER_PASSWORD),
    ):
        create_or_update_secret(
            namespace,
            name=user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": pwd},
            api_client=central_cluster_client,
        )
        user.update()

    admin_user.assert_reaches_phase(Phase.Updated, timeout=600)
    mdb_user.assert_reaches_phase(Phase.Updated, timeout=600)
    mongot_user.assert_reaches_phase(Phase.Updated, timeout=600)

    _replicate_mongot_user_password_to_members(
        namespace, central_cluster_client, member_cluster_clients, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_simulated_mc_sharded_basic
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    """Install sim ops after MongoDB Running + users Updated: sim-op install perturbs member-cluster state and races central-op reconcile + AC propagation."""
    member_cluster_clients = member_cluster_clients[:2]
    base_helm_args = dict(multi_cluster_operator_installation_config)
    for mcc in member_cluster_clients:
        operator = _install_simulated_operator(namespace, base_helm_args, mcc)
        operator.assert_is_running()


@mark.e2e_search_simulated_mc_sharded_basic
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


@mark.e2e_search_simulated_mc_sharded_basic
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


@mark.e2e_search_simulated_mc_sharded_basic
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material must be present locally."""
    member_cluster_clients = member_cluster_clients[:2]
    central_core = CoreV1Api(api_client=central_cluster_client)

    # Cluster-agnostic Secrets — same copy to every member cluster.
    shared_secret_names = [
        search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
    ]
    for secret_name in shared_secret_names:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in member_cluster_clients:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # Per-(cluster, shard) mongot certs — each cluster only needs its own per-shard certs.
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            secret_name = search_resource_names.shard_tls_cert_name(
                MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=ci
            )
            secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
            data = read_secret(namespace, secret_name, api_client=central_cluster_client)
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)
        logger.info(f"replicated per-shard mongot Secrets to {mcc.cluster_name} (cluster_index={ci})")


# ---------------------------------------------------------------------------
# Search CR lifecycle
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_search_cr_reaches_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Same CR applied to each member; each simulated operator stamps Running on its own slice."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.update()
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
        logger.info(f"MongoDBSearch {mdbs.name} Running in {mcc.cluster_name}")


@mark.e2e_search_simulated_mc_sharded_basic
def test_per_cluster_sharded_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each member materialises its own (cluster, shard) mongot STS/Svc/CM/proxy + per-cluster Envoy LB."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    for mcc, _mdbs in per_cluster_mdbs_search:
        ci = _idx(mcc)
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
            mcc.read_namespaced_config_map(cm_name, namespace)
            logger.info(
                f"[{mcc.cluster_name}] (cluster_index={ci}, shard={shard_name}): "
                f"sts={sts_name} svc={svc_name} proxy={proxy_svc_name} cm={cm_name}"
            )

        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        envoy_dep = mcc.apps_v1_api().read_namespaced_deployment(name=envoy_dep_name, namespace=namespace)
        assert envoy_dep.spec.replicas == ENVOY_LB_REPLICAS, (
            f"[{mcc.cluster_name}] {envoy_dep_name} expected {ENVOY_LB_REPLICAS} replicas, "
            f"got {envoy_dep.spec.replicas}"
        )
        logger.info(f"[{mcc.cluster_name}] envoy_lb={envoy_dep_name} (replicas={ENVOY_LB_REPLICAS})")


@mark.e2e_search_simulated_mc_sharded_basic
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """No cross-cluster status awareness in simulated-MC mode; each cluster reports its own slice's phase."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        phase = mdbs.get_status_phase()
        assert phase == Phase.Running, f"[{mcc.cluster_name}] MongoDBSearch {mdbs.name} phase={phase}, expected Running"


# ---------------------------------------------------------------------------
# Data plane: per-cluster $search via local mongos (directConnection=true)
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_restore_sample_database(namespace: str, tools_pod: ToolsPod):
    """Restore sample_mflix via cluster-0's mongos (logical cluster, so cluster-1's mongos sees same data)."""
    tester = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_simulated_mc_sharded_basic
def test_shard_sample_collection(namespace: str):
    """Hash-shard sample_mflix.movies; skip reshardCollection (hangs in simulated-MC)."""
    admin = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    ns = "sample_mflix.movies"
    admin.client["sample_mflix"]["movies"].create_index([("_id", pymongo.HASHED)])
    try:
        admin.client.admin.command("shardCollection", ns, key={"_id": "hashed"}, unique=False)
    except pymongo.errors.OperationFailure as exc:
        if "already sharded" in str(exc) or exc.code == 20:
            logger.info(f"{ns} already sharded")
        else:
            raise
    logger.info(f"{ns} hash-sharded (skipping reshardCollection redistribution)")


@mark.e2e_search_simulated_mc_sharded_basic
def test_create_search_index(namespace: str):
    """Create movies $search index (cluster-level; per-shard mongots pick it up via configsrv watch)."""
    tester = _per_cluster_mongos_search_tester(namespace, 0, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_sharded_basic
def test_per_cluster_search_query(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search via each cluster's local mongos.

    Honesty note: this exercises ONLY cluster-0's data path. In MC sharded mode both
    mongos share a single spec.mongos.additionalMongodConfig (one mongotHost value), so
    each cluster's mongos routes mongot traffic to cluster-0's Envoy via Istio east-west.
    Querying through each cluster's mongos therefore proves per-cluster mongos
    connectivity, but the per-shard mongot data path that actually serves the rows is
    cluster-0's — cluster-1's per-shard mongots are not exercised by these queries.
    test_mongos_mongot_host_single_path makes this single-path reality explicit on disk.
    """
    member_cluster_clients = member_cluster_clients[:2]
    assert_per_cluster_count(member_cluster_clients)
    for mcc in member_cluster_clients:
        cluster_index = _idx(mcc)
        tester = _per_cluster_mongos_search_tester(namespace, cluster_index, USER_NAME, USER_PASSWORD)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        def execute_search(_ci: int = cluster_index) -> tuple:
            try:
                results = movies.text_search_movies("Star Wars")
                count = len(results)
                if count > 0:
                    titles = [r.get("title") for r in results[:5]]
                    logger.info(f"cluster_index={_ci}: $search returned {count} results: {titles}")
                    return True, f"cluster_index={_ci}: $search returned {count} results"
                return False, f"cluster_index={_ci}: $search returned no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster_index={_ci}: $search error: {exc}"

        run_periodically(
            execute_search,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            sleep_time=5,
            msg=f"cluster_index={cluster_index}: $search via local mongos",
        )


# ---------------------------------------------------------------------------
# Single-path read-back + cross-cluster isolation + persisted state
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_sharded_basic
def test_mongos_mongot_host_single_path(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Read-back proving the single-path reality: BOTH clusters' mongos report mongotHost
    pointing at cluster-0's Envoy proxy endpoint.

    MC sharded mongos share one spec.mongos.additionalMongodConfig, so the operator stamps
    the same cluster-0 mongotHost on every mongos. We read it via `getParameter` over each
    cluster's local mongos (directConnection) rather than the on-disk automation-mongod.conf
    because mongos is stateless and has no automation-mongod.conf. expected_by_idx is
    {0: ep0, 1: ep0} — the explicit assertion that cluster-1's mongos is NOT cluster-local.
    """
    member_cluster_clients = member_cluster_clients[:2]
    assert_per_cluster_count(member_cluster_clients)

    cluster0_endpoint = (
        f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, 0)}:{ENVOY_PROXY_PORT}"
    )
    expected_by_idx = {_idx(mcc): cluster0_endpoint for mcc in member_cluster_clients}

    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        tester = _per_cluster_mongos_search_tester(namespace, ci, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
        params = tester.client.admin.command(
            {"getParameter": 1, "mongotHost": 1, "searchIndexManagementHostAndPort": 1}
        )
        got_host = params.get("mongotHost", "")
        got_idx_mgmt = params.get("searchIndexManagementHostAndPort", "")
        assert got_host == expected_by_idx[ci], (
            f"[{mcc.cluster_name}] mongos mongotHost={got_host!r}, expected cluster-0 endpoint "
            f"{expected_by_idx[ci]!r} (both mongos share one additionalMongodConfig → single path)"
        )
        assert got_idx_mgmt == expected_by_idx[ci], (
            f"[{mcc.cluster_name}] mongos searchIndexManagementHostAndPort={got_idx_mgmt!r}, "
            f"expected cluster-0 endpoint {expected_by_idx[ci]!r}"
        )
        logger.info(f"[{mcc.cluster_name}] mongos mongotHost={got_host} (cluster-0 single-path confirmed)")


@mark.e2e_search_simulated_mc_sharded_basic
def test_cross_cluster_isolation_absence(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each cluster must NOT contain the OTHER cluster's per-(cluster,shard) Search resources.
    Confirms each simulated operator only materialises its own LocalizeToCluster slice.
    """
    assert_per_cluster_count(per_cluster_mdbs_search)
    shard_names = [f"{MDB_RESOURCE_NAME}-{shard_idx}" for shard_idx in range(SHARD_COUNT)]
    indices = [_idx(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, _mdbs in per_cluster_mdbs_search:
        this_idx = _idx(mcc)
        for foreign_idx in indices:
            if foreign_idx == this_idx:
                continue
            assert_foreign_resources_absent(
                mcc,
                MDBS_RESOURCE_NAME,
                foreign_idx,
                namespace,
                sharded=True,
                shard_names=shard_names,
            )


@mark.e2e_search_simulated_mc_sharded_basic
def test_state_configmap_mapping_persisted(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each cluster's `{mdbs}-state` ConfigMap pins clusterMapping[thisCluster]==thisIndex.

    Simulated-MC narrows spec.clusters[] to the local slice (LocalizeToCluster) before
    persisting, so the mapping is LOCAL: it holds this cluster's entry only and must NOT
    leak the foreign cluster's name. This is the per-cluster index-pinning source of truth.
    """
    assert_per_cluster_count(per_cluster_mdbs_search)
    names = {mcc.cluster_name for mcc, _ in per_cluster_mdbs_search}
    for mcc, _mdbs in per_cluster_mdbs_search:
        mapping = read_state_configmap_mapping(mcc, MDBS_RESOURCE_NAME, namespace)
        assert mapping.get(mcc.cluster_name) == _idx(mcc), (
            f"[{mcc.cluster_name}] state clusterMapping[{mcc.cluster_name!r}]={mapping.get(mcc.cluster_name)!r}, "
            f"want {_idx(mcc)}"
        )
        for other in names - {mcc.cluster_name}:
            assert other not in mapping, (
                f"[{mcc.cluster_name}] state clusterMapping leaked foreign cluster {other!r}: {mapping} — "
                f"simulated-MC operators persist a LOCAL-only mapping"
            )
        logger.info(f"[{mcc.cluster_name}] persisted local clusterMapping={mapping}")
