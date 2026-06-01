"""Simulated multi-cluster MongoDBSearch e2e: 3 operators (1 central + 2 simulated-MC),
cross-cluster RS source via MongoDBMultiCluster, TLS everywhere, asserts each
simulated operator only materialises its own spec.clusters[i] slice."""

from __future__ import annotations

import os
from typing import List, Tuple

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
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
    assert_mongot_host_on_disk,
    assert_per_cluster_count,
    per_cluster_search_tester,
    read_state_configmap_mapping,
)

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

MDB_RESOURCE_NAME = "mdb-mc-sim"
MDBS_RESOURCE_NAME = "mdb-mc-sim-search"

MEMBERS_PER_CLUSTER: List[int | None] = [3, 3]
MONGOT_REPLICAS_PER_CLUSTER = 3

# Generous index-ready timeout to account for cross-cluster mesh latency during sync.
SEARCH_INDEX_READY_TIMEOUT = 300
# mongot may still be in INITIAL_SYNC briefly after the index reports READY.
SEARCH_QUERY_RETRY_TIMEOUT = 90

# --- TLS-everywhere constants ----------------------------------------------
# CA ConfigMap (used by MongoDBMulti.spec.security.tls.ca — key: ca-pem/mms-ca.crt).
# create_issuer_ca writes BOTH a ConfigMap and a Secret with the same name; the
# Secret half (key: ca.crt) is consumed by spec.source.external.tls.ca on the
# MongoDBSearch CR.
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
# Source RS bundle Secret name follows the central MC operator's convention:
# `{prefix}-{name}-cert`. create_multi_cluster_mongodb_tls_certs uses this name.
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"


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
    """Central MC operator scoped to MongoDB/User/MultiCluster (NOT mongodbsearch)."""
    from tests.conftest import (
        MULTI_CLUSTER_OPERATOR_NAME,
        _install_multi_cluster_operator,
        local_operator,
        run_kube_config_creation_tool,
    )

    # Kind e2e variant exposes 3 member clusters; this test exercises a 2-cluster
    # simulated-MC topology, so restrict to the first two members.
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
            "operator.watchedResources": "{mongodb,opsmanagers,mongodbusers,mongodbcommunity,mongodbmulticluster}",
        },
        central_cluster_name,
    )


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """Write the issuer CA as both ConfigMap (ca-pem/mms-ca.crt — MongoDBMulti) and Secret (ca.crt — MongoDBSearch source)."""
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    ca_configmap: str,
) -> MongoDBMulti:
    """6-member cross-cluster ReplicaSet (3+3) with TLS+SCRAM; applied only to the central cluster."""
    # Kind e2e variant exposes 3 member clusters; this test exercises a 2-cluster
    # simulated-MC topology, so restrict to the first two members.
    member_cluster_names = member_cluster_names[:2]
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("simulated-mc-mongodb.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)
    resource["spec"]["additionalMongodConfig"] = {
        "setParameter": {
            "skipAuthenticationToSearchIndexManagementServer": False,
            "skipAuthenticationToMongot": False,
            "searchTLSMode": "requireTLS",
            "useGrpcForSearch": True,
        },
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
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR (spec.clusters lists all clusters) applied to each member's API; each operator projects to its own slice."""
    # Kind e2e variant exposes 3 member clusters; this test exercises a 2-cluster
    # simulated-MC topology, so restrict to the first two members.
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

    # Seed list: every cross-cluster RS member FQDN (Istio MC east-west routing
    # makes every cluster's DNS resolve every other cluster's mongod Service).
    seeds = [
        f"{MDB_RESOURCE_NAME}-{c}-{m}-svc.{namespace}.svc.cluster.local:27017"
        for c, members in enumerate(MEMBERS_PER_CLUSTER[: len(member_cluster_clients)])
        if members
        for m in range(members)
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
                "externalHostname": (
                    f"{MDBS_RESOURCE_NAME}-search-{{clusterIndex}}-proxy-svc.{namespace}.svc.cluster.local"
                ),
            },
        }
        mdbs["spec"]["source"] = {
            "username": MONGOT_USER_NAME,
            "passwordSecretRef": {
                "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
                "key": "password",
            },
            "external": {
                "hostAndPorts": seeds,
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
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_basic
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.assert_is_running()


@mark.e2e_search_simulated_mc_basic
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_clients = member_cluster_clients[:2]
    base_helm_args = dict(multi_cluster_operator_installation_config)
    for mcc in member_cluster_clients:
        operator = _install_simulated_operator(namespace, base_helm_args, mcc)
        operator.assert_is_running()


@mark.e2e_search_simulated_mc_basic
def test_install_source_tls_certificates(
    multi_cluster_issuer: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    member_cluster_clients = member_cluster_clients[:2]
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        member_cluster_clients,
        central_cluster_client,
        mdb,
    )


@mark.e2e_search_simulated_mc_basic
def test_mongodb_running(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_simulated_mc_basic
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
    member_cluster_clients: List[MultiClusterClient],
):
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


@mark.e2e_search_simulated_mc_basic
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_clients = member_cluster_clients[:2]
    lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)

    server_domains = [
        f"{MDBS_RESOURCE_NAME}-search-{_idx(mcc)}-proxy-svc.{namespace}.svc.cluster.local"
        for mcc in member_cluster_clients
    ]

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, 0),
        replicas=ENVOY_LB_REPLICAS,
        service_name=server_domains[0].split(".")[0],
        additional_domains=server_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, 0)}-client",
        replicas=1,
        service_name=server_domains[0].split(".")[0],
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


@mark.e2e_search_simulated_mc_basic
def test_create_search_tls_certificate(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """mongot TLS cert MUST carry per-cluster SANs; cert for cluster-0 fails the handshake on cluster-1 etc."""
    member_cluster_clients = member_cluster_clients[:2]
    secret_name = search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    additional_domains: List[str] = []
    for mcc in member_cluster_clients:
        idx = _idx(mcc)
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-svc.{namespace}.svc.cluster.local")
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc.{namespace}.svc.cluster.local")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"mongot TLS certificate created with SANs={additional_domains}: {secret_name}")


@mark.e2e_search_simulated_mc_basic
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material must be present locally."""
    member_cluster_clients = member_cluster_clients[:2]
    central_core = CoreV1Api(api_client=central_cluster_client)

    secrets_to_replicate = [
        search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        # CA stored as both ConfigMap and Secret (create_issuer_ca writes both).
        # Simulated operator's source.external.tls.ca reference resolves to the
        # Secret half (key: ca.crt) — must be on every member.
        CA_CONFIGMAP_NAME,
    ]
    for secret_name in secrets_to_replicate:
        secret_data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in member_cluster_clients:
            create_or_update_secret(namespace, secret_name, secret_data, api_client=mcc.api_client)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # CA ConfigMap half (key: ca-pem / mms-ca.crt) — for any future MongoDBMulti
    # or Search code path that reads the CA from ConfigMap rather than Secret.
    # Cheap to keep in sync with the Secret.
    source_cm = central_core.read_namespaced_config_map(name=CA_CONFIGMAP_NAME, namespace=namespace)
    cm_data = dict(source_cm.data or {})
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, cm_data, api_client=mcc.api_client)
        logger.info(f"replicated CA ConfigMap {CA_CONFIGMAP_NAME} into cluster {mcc.cluster_name}")


@mark.e2e_search_simulated_mc_basic
def test_search_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_per_cluster_count(per_cluster_mdbs_search)
    for _mcc, mdbs in per_cluster_mdbs_search:
        mdbs.update()
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
        logger.info(f"MongoDBSearch {mdbs.name} Running in {mcc.cluster_name}")


@mark.e2e_search_simulated_mc_basic
def test_per_cluster_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_per_cluster_count(per_cluster_mdbs_search)
    for mcc, _mdbs in per_cluster_mdbs_search:
        idx = _idx(mcc)
        sts_name = f"{MDBS_RESOURCE_NAME}-search-{idx}"
        svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-svc"
        proxy_svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc"
        mongot_cm_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-config"
        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx)

        sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
        assert sts.spec.replicas == MONGOT_REPLICAS_PER_CLUSTER, (
            f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, "
            f"got {sts.spec.replicas}"
        )
        mcc.read_namespaced_service(svc_name, namespace)
        mcc.read_namespaced_service(proxy_svc_name, namespace)
        mcc.read_namespaced_config_map(mongot_cm_name, namespace)
        envoy_dep = mcc.apps_v1_api().read_namespaced_deployment(name=envoy_dep_name, namespace=namespace)
        assert envoy_dep.spec.replicas == ENVOY_LB_REPLICAS, (
            f"[{mcc.cluster_name}] {envoy_dep_name} expected {ENVOY_LB_REPLICAS} replicas, "
            f"got {envoy_dep.spec.replicas}"
        )
        logger.info(
            f"[{mcc.cluster_name}] sts={sts_name} (replicas={MONGOT_REPLICAS_PER_CLUSTER}) "
            f"svc={svc_name} proxy={proxy_svc_name} cm={mongot_cm_name} "
            f"envoy_lb={envoy_dep_name} (replicas={ENVOY_LB_REPLICAS})"
        )


@mark.e2e_search_simulated_mc_basic
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """No cross-cluster status awareness in simulated-MC mode; each cluster's CR view reports its own phase."""
    assert_per_cluster_count(per_cluster_mdbs_search)
    for mcc, mdbs in per_cluster_mdbs_search:
        mdbs.load()
        phase = mdbs.get_status_phase()
        assert phase == Phase.Running, f"[{mcc.cluster_name}] MongoDBSearch {mdbs.name} phase={phase}, expected Running"


# ---------------------------------------------------------------------------
# Per-cluster mongotHost locality (the unique value of the LocalizeToCluster topology)
# ---------------------------------------------------------------------------


def _expected_mongot_host_by_idx(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> dict:
    """cluster_index -> THAT cluster's LOCAL Envoy proxy-svc FQDN:port.

    Each entry is the cluster's own proxy svc — NOT a single shared cluster's — so
    a regression that funnels every mongod through one cluster's Envoy is caught.
    """
    return {
        _idx(mcc): (
            f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, _idx(mcc))}:{ENVOY_PROXY_PORT}"
        )
        for mcc in member_cluster_clients
    }


@mark.e2e_search_simulated_mc_basic
def test_patch_per_cluster_mongot_host(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """Patch the OM automation config so each cluster's mongods use THEIR OWN cluster-local
    Envoy proxy-svc FQDN for mongotHost + searchIndexManagementHostAndPort.

    MongoDBMultiCluster does not expose per-cluster additionalMongodConfig, so (exactly
    as q2_mc_rs_steady) the per-process AC keys are set directly. Leaving them out of the
    CR spec ensures subsequent operator reconciles never clobber this per-cluster locality.
    """
    member_cluster_clients = member_cluster_clients[:2]
    expected_by_idx = _expected_mongot_host_by_idx(namespace, member_cluster_clients)
    logger.info(f"per-cluster mongotHost map: {expected_by_idx}")

    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    process_prefix = f"{mdb.name}-"
    patched_processes: List[str] = []
    for process in ac.get("processes", []):
        process_name = process.get("name", "")
        if not process_name.startswith(process_prefix):
            continue
        try:
            cluster_idx = int(process_name[len(process_prefix) :].split("-")[0])
        except ValueError:
            continue
        if cluster_idx not in expected_by_idx:
            continue

        mongot_host = expected_by_idx[cluster_idx]
        set_parameter = process.setdefault("args2_6", {}).setdefault("setParameter", {})
        set_parameter["mongotHost"] = mongot_host
        set_parameter["searchIndexManagementHostAndPort"] = mongot_host
        patched_processes.append(f"{process_name}->{mongot_host}")

    assert patched_processes, (
        f"no AC processes matched prefix {process_prefix!r}; "
        f"AC contained {[p.get('name') for p in ac.get('processes', [])]}"
    )
    logger.info(f"patched {len(patched_processes)} processes: {patched_processes}")

    ac["version"] = ac.get("version", 0) + 1
    om_tester.om_request("put", ac_path, json_object=ac)
    logger.info(f"PUT automation config v{ac['version']} with per-cluster mongotHost")

    # Block until every mongod applies the new goal version — setParameter changes restart processes.
    om_tester.wait_agents_ready(timeout=900)


@mark.e2e_search_simulated_mc_basic
def test_per_cluster_mongot_host_observed(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """On-disk read-back: each cluster's first mongod must show ITS OWN cluster-local
    proxy-svc FQDN in mongotHost + searchIndexManagementHostAndPort. Proves per-cluster
    locality landed AND was applied by the agent (not just accepted by OM REST).
    """
    member_cluster_clients = member_cluster_clients[:2]
    expected_by_idx = _expected_mongot_host_by_idx(namespace, member_cluster_clients)
    assert_mongot_host_on_disk(mdb, expected_by_idx, member_cluster_clients)


# ---------------------------------------------------------------------------
# Data plane: seed data + $search index (once on source RS) + per-cluster query
# ---------------------------------------------------------------------------


def _source_rs_search_tester(mdb: MongoDBMulti, username: str, password: str) -> SearchTester:
    """SearchTester pointed at the source MongoDBMulti RS via mesh DNS, using
    ?replicaSet=... so the driver discovers the primary for writes (restore + index).
    """
    seed_host = f"{mdb.name}-0-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={mdb.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_simulated_mc_basic
def test_restore_sample_database(mdb: MongoDBMulti, tools_pod):
    """mongorestore sample_mflix into the source RS (a single logical RS; every
    per-cluster mongot syncs from it, so one restore feeds both clusters' mongots).
    """
    tester = _source_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_simulated_mc_basic
def test_create_search_index(mdb: MongoDBMulti):
    """Create the $search index ONCE against the source RS — mongot replication
    propagates it to every per-cluster mongot. (Per-cluster applies to the QUERY, not
    the index: creating it N times via direct connections would be redundant.)
    """
    tester = _source_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_basic
def test_per_cluster_search_query(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search direct-connected to EACH cluster's own mongod — proves each cluster's
    independent mongod -> local-Envoy -> local-mongot path returns rows. This is the
    unique value of the per-operator LocalizeToCluster topology: not just the cluster
    currently holding the primary, but every cluster's local data path.
    """
    member_cluster_clients = member_cluster_clients[:2]
    assert_per_cluster_count(member_cluster_clients)

    for mcc in member_cluster_clients:
        cluster_index = _idx(mcc)
        pod_host = f"{mdb.name}-{cluster_index}-0-svc.{mdb.namespace}.svc.cluster.local:27017"
        tester = per_cluster_search_tester(pod_host, USER_NAME, USER_PASSWORD, direct=True)
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
            msg=f"cluster_index={cluster_index}: $search via local mongod",
        )


# ---------------------------------------------------------------------------
# Cross-cluster isolation + persisted state
# ---------------------------------------------------------------------------


@mark.e2e_search_simulated_mc_basic
def test_cross_cluster_isolation_absence(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each cluster must NOT contain the OTHER cluster's index-suffixed Search resources.
    Confirms each simulated operator only materialises its own LocalizeToCluster slice.
    """
    assert_per_cluster_count(per_cluster_mdbs_search)
    indices = [_idx(mcc) for mcc, _ in per_cluster_mdbs_search]
    for mcc, _mdbs in per_cluster_mdbs_search:
        this_idx = _idx(mcc)
        for foreign_idx in indices:
            if foreign_idx == this_idx:
                continue
            assert_foreign_resources_absent(mcc, MDBS_RESOURCE_NAME, foreign_idx, namespace)


@mark.e2e_search_simulated_mc_basic
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
        foreign = names - {mcc.cluster_name}
        for other in foreign:
            assert other not in mapping, (
                f"[{mcc.cluster_name}] state clusterMapping leaked foreign cluster {other!r}: {mapping} — "
                f"simulated-MC operators persist a LOCAL-only mapping"
            )
        logger.info(f"[{mcc.cluster_name}] persisted local clusterMapping={mapping}")
