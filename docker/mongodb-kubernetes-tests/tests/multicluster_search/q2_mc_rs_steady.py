"""
Q2-MC MongoDBSearch e2e: multi-cluster, ReplicaSet source, real $search data plane.

Topology contract (Q2-MC RS):
- Source: an Enterprise MongoDBMulti ReplicaSet across len(member_cluster_clients)
  member clusters with TLS+SCRAM. Each cluster's `memberConfig[].tags.region` is
  pinned to REGION_TAGS[i] so MongoDBSearch's per-cluster
  `syncSourceSelector.matchTags.region` can pick the local member as sync source.
- mongotHost (set on the source via spec.additionalMongodConfig.setParameter) is
  the operator-managed Envoy proxy *Service* hostname. The Service name is the
  same in every cluster (`{name}-search-0-proxy-svc`); per-cluster differentiation
  lives at the *Deployment*/*ConfigMap* level (B16:
  `LoadBalancerDeploymentNameForCluster` -> `{name}-search-lb-0-{clusterName}`).
  So a single mongotHost value works for all members; in-cluster DNS resolves to
  the local Envoy Deployment.
- MongoDBSearch uses the *external* source contract, NOT mongodbResourceRef:
  `len(clusters) > 1` is rejected by the internal-source validator
  (controllers/searchcontroller/enterprise_search_source.go:69-71). The external
  path (ExternalSearchSource / ShardedExternalSearchSource) does not enforce
  single-cluster topology.
- `external.hostAndPorts` is built from MongoDBMulti's per-cluster, per-pod
  Service FQDN pattern (`{mdb}-{clusterIdx}-{podIdx}-svc.{ns}.svc.cluster.local`),
  one entry per (cluster, pod). Cross-cluster reachability is provided by the
  standard MC test harness (Istio mesh) — same as multi_cluster_scram.py.

Asserts:
- MongoDBMulti source reaches Phase=Running.
- MongoDBSearch reaches Phase=Running with status.clusterStatusList populated
  (one entry per member cluster, all Running).
- spec.loadBalancer.managed status is consistent (assert_lb_status).
- The per-cluster Envoy Deployment in every member cluster has >=1 ready replica.
- Sample mflix data is restored via mongorestore against the RS.
- A `default` search index is created on sample_mflix.movies and reaches READY.
- A $search aggregation seeded from *each member cluster's local pod* returns
  non-empty results. Note: `?replicaSet=` topology discovery routes the
  aggregate to the primary regardless of seed cluster, so this proves $search
  is end-to-end functional but does not isolate per-cluster mongot/Envoy
  behaviour. Strict per-cluster routing assertions would require
  `?directConnection=true&readPreference=secondaryPreferred`, which is left as
  a follow-up.
"""

from typing import List

import kubernetes
from kubetester import create_or_update_secret, try_load
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.q2_shared import (
    ADMIN_USER_NAME,
    ADMIN_USER_PASSWORD,
    ENVOY_PROXY_PORT,
    MDBS_TLS_CERT_PREFIX,
    MONGOT_USER_NAME,
    MONGOT_USER_PASSWORD,
    USER_NAME,
    USER_PASSWORD,
    q2_create_search_index,
    q2_restore_sample,
)
from tests.common.search.q2_topology import REGION_TAGS
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
)
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca, verify_text_search_query
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_search.helpers import assert_envoy_ready_in_each_cluster

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-rs-q2-mc"
MDBS_RESOURCE_NAME = "mdb-rs-q2-mc-search"

MDB_BUNDLE_SECRET_PREFIX = "certs"
MDB_BUNDLE_SECRET_NAME = f"{MDB_BUNDLE_SECRET_PREFIX}-{MDB_RESOURCE_NAME}-cert"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

# 1 member per cluster: keep the cluster topology compact and the test fast.
# Each cluster's single member carries that cluster's region tag, so the
# per-cluster syncSourceSelector picks it deterministically.
MEMBERS_PER_CLUSTER = 1


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    """Issuer-CA ConfigMap consumed by the source MongoDBMulti and the MDBS external TLS block."""
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb_unmarshalled(
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    """MongoDBMulti scaffold; clusterSpecList carries region tags per member cluster."""
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE_NAME, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))

    # Per-cluster memberConfig: one member per cluster, region tag pinned to
    # REGION_TAGS[i]. The MongoDBSearch CR's clusters[i].syncSourceSelector
    # matches the same region tag at the same index — see `mdbs` fixture.
    member_options = [
        [{"votes": 1, "priority": "1.0", "tags": {"region": REGION_TAGS[i]}}]
        for i in range(len(member_cluster_names))
    ]
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names,
        [MEMBERS_PER_CLUSTER] * len(member_cluster_names),
        member_options,
    )
    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mdb_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
) -> str:
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        MDB_BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mdb_unmarshalled,
    )


@fixture(scope="module")
def mdb(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    mdb_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
    namespace: str,
) -> MongoDBMulti:
    """Source MongoDBMulti: TLS, SCRAM, mongotHost = operator-managed Envoy proxy Service.

    On rerun, try_load returns the on-cluster spec and we leave it alone — same
    pattern as multi_cluster_tls_with_scram.py.
    """
    resource = mdb_unmarshalled
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    if try_load(resource):
        return resource

    proxy_host = search_resource_names.proxy_service_host(MDBS_RESOURCE_NAME, namespace, ENVOY_PROXY_PORT)
    resource["spec"]["security"] = {
        "certsSecretPrefix": MDB_BUNDLE_SECRET_PREFIX,
        "tls": {"ca": multi_cluster_issuer_ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }
    # Single mongotHost is correct for MC: B16 keeps the proxy Service name the
    # same in every cluster — DNS resolves to the local Envoy Deployment.
    resource["spec"]["additionalMongodConfig"] = {
        "setParameter": {
            "mongotHost": proxy_host,
            "searchIndexManagementHostAndPort": proxy_host,
            "skipAuthenticationToSearchIndexManagementServer": False,
            "skipAuthenticationToMongot": False,
            "searchTLSMode": "requireTLS",
            "useGrpcForSearch": True,
        }
    }
    return resource


def _user_resource(
    fixture_filename: str,
    name: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    *,
    username: str | None = None,
) -> MongoDBUser:
    """Build a MongoDBUser CR pointed at the MongoDBMulti source.

    `MongoDBUser.spec.mongodbResourceRef` resolves by name regardless of whether
    the target is a MongoDB or MongoDBMultiCluster — see multi_cluster_scram.py.

    On rerun (resource exists): try_load returns the on-cluster spec verbatim and
    we skip mutations to avoid clobbering whatever the operator may have written.
    On first run: try_load is a no-op and we apply the desired spec.
    """
    resource = MongoDBUser.from_yaml(yaml_fixture(fixture_filename), name, namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    if try_load(resource):
        return resource
    resource["spec"]["username"] = username or name
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["passwordSecretKeyRef"] = {"name": f"{name}-password", "key": "password"}
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _user_resource("mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def app_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _user_resource("mongodbuser-mdb-user.yaml", USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    name = f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}"
    return _user_resource(
        "mongodbuser-search-sync-source-user.yaml",
        name,
        namespace,
        central_cluster_client,
        username=MONGOT_USER_NAME,
    )


def _seed_pod_fqdn(mdb_name: str, namespace: str, cluster_index: int, pod_index: int = 0) -> str:
    """Per-cluster, per-pod Service FQDN — same pattern certs/operator use
    (mirrors `multi_cluster_service_fqdns` at kubetester/certs.py:345).
    """
    return f"{mdb_name}-{cluster_index}-{pod_index}-svc.{namespace}.svc.cluster.local"


def _build_mdb_seeds(mdb_resource: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]) -> List[str]:
    """One host:port per (cluster, pod) for spec.source.external.hostAndPorts."""
    return [
        f"{_seed_pod_fqdn(mdb_resource.name, mdb_resource.namespace, mcc.cluster_index, pod_idx)}:27017"
        for mcc in member_cluster_clients
        for pod_idx in range(mdb_resource.get_item_spec(mcc.cluster_name)["members"])
    ]


@fixture(scope="module")
def mdbs(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
) -> MongoDBSearch:
    """MongoDBSearch CR using the *external* source contract (required for len(clusters) > 1)."""
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("q2-mc-rs.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    if try_load(resource):
        return resource

    # External source: per-pod-svc FQDNs + TLS CA + SCRAM creds for mongot.
    # Internal mongodbResourceRef is rejected for MC topology — see file docstring.
    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "hostAndPorts": _build_mdb_seeds(mdb, member_cluster_clients),
            "tls": {"ca": {"name": CA_CONFIGMAP_NAME}},
        },
    }
    resource["spec"]["security"] = {"tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX}}

    # One spec.clusters[] entry per member cluster, region tag matched to the
    # source MongoDBMulti's per-member region tag at the same index.
    # Don't set top-level spec.replicas — clusters[] is exclusive with the
    # deprecated top-level distribution fields (B14+B18 admission validators).
    resource["spec"]["clusters"] = [
        {
            "clusterName": mcc.cluster_name,
            "replicas": 1,
            "syncSourceSelector": {"matchTags": {"region": REGION_TAGS[i]}},
        }
        for i, mcc in enumerate(member_cluster_clients)
    ]
    return resource


# ---------------------------------------------------------------------------
# Test sequence
# ---------------------------------------------------------------------------


@mark.e2e_search_q2_mc_rs_steady
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_search_q2_mc_rs_steady
def test_install_tls_certificates(server_certs: str, ca_configmap: str):
    """Force-materialize the MongoDBMulti TLS bundle and issuer-CA ConfigMap before the MDB resource is created."""
    logger.info(f"MongoDBMulti TLS bundle: {server_certs}; CA ConfigMap: {ca_configmap}")


@mark.e2e_search_q2_mc_rs_steady
def test_create_database_resource(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_q2_mc_rs_steady
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    app_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    """Create the admin/app users on the source RS, plus the mongot sync-source user.

    The mongot user is created without waiting for Updated — the searchCoordinator
    role only exists after the MongoDBSearch resource is reconciled.
    """
    for user, password in (
        (admin_user, ADMIN_USER_PASSWORD),
        (app_user, USER_PASSWORD),
        (mongot_user, MONGOT_USER_PASSWORD),
    ):
        create_or_update_secret(
            namespace=namespace,
            name=user["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": password},
            api_client=central_cluster_client,
        )
        user.update()

    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
    app_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_q2_mc_rs_steady
def test_deploy_lb_certificates(namespace: str, multi_cluster_issuer: str):
    """LB server/client certs for the managed Envoy proxy.

    The proxy Service name is shared across clusters (only the Deployment is
    per-cluster in B16), so the SC helper covers it with a single SAN.
    """
    create_rs_lb_certificates(namespace, multi_cluster_issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_tls_certificate(namespace: str, multi_cluster_issuer: str):
    create_rs_search_tls_cert(namespace, multi_cluster_issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch, member_cluster_clients: List[MultiClusterClient]):
    mdbs.load()
    cluster_statuses = mdbs["status"]["clusterStatusList"]["clusterStatuses"]
    assert len(cluster_statuses) == len(member_cluster_clients), (
        f"expected {len(member_cluster_clients)} cluster status entries, got {len(cluster_statuses)}"
    )
    cluster_names = {mcc.cluster_name for mcc in member_cluster_clients}
    for cs in cluster_statuses:
        assert cs["clusterName"] in cluster_names, f"unexpected clusterName: {cs['clusterName']}"
        assert cs["phase"] == "Running", f"cluster {cs['clusterName']}: phase={cs['phase']}, expected Running"


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_envoy_deployment(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    assert_envoy_ready_in_each_cluster(namespace, MDBS_RESOURCE_NAME, member_cluster_clients)


@mark.e2e_search_q2_mc_rs_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    mdbs.load()
    mdbs.assert_lb_status()


# ---------------------------------------------------------------------------
# Data-plane verification
# ---------------------------------------------------------------------------


def _mc_search_tester_factory(seed_pod_fqdn: str, ca_path: str):
    """TesterFactory pinned to a MongoDBMulti pod-Service FQDN.

    `SearchTester.for_replicaset` hard-codes the SC RS pod-0 FQDN
    (`{name}-0.{name}-svc...`) which doesn't exist on MongoDBMulti, so it can't
    be reused. `?replicaSet=` lets pymongo discover the primary from any
    reachable seed.
    """

    def factory(_mdb, username: str, password: str, use_ssl: bool):
        conn = (
            f"mongodb://{username}:{password}@{seed_pod_fqdn}:27017/"
            f"?replicaSet={_mdb.name}"
        )
        return SearchTester(conn, use_ssl=use_ssl, ca_path=ca_path if use_ssl else None)

    return factory


@mark.e2e_search_q2_mc_rs_steady
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_q2_mc_rs_steady
def test_restore_sample_database(
    namespace: str,
    mdb: MongoDBMulti,
    tools_pod: mongodb_tools_pod.ToolsPod,
    member_cluster_clients: List[MultiClusterClient],
    issuer_ca_filepath: str,
):
    """Run mongorestore once against the RS — replication carries data to all clusters.

    Seed via the first member cluster's pod-0; pymongo's RS discovery finds the
    primary and routes the writes there.
    """
    seed = _seed_pod_fqdn(mdb.name, namespace, member_cluster_clients[0].cluster_index)
    q2_restore_sample(mdb, tools_pod, _mc_search_tester_factory(seed, issuer_ca_filepath))


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_index(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    issuer_ca_filepath: str,
):
    seed = _seed_pod_fqdn(mdb.name, namespace, member_cluster_clients[0].cluster_index)
    q2_create_search_index(mdb, _mc_search_tester_factory(seed, issuer_ca_filepath))


@mark.e2e_search_q2_mc_rs_steady
def test_execute_text_search_query_per_cluster(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    issuer_ca_filepath: str,
):
    """Execute a $search aggregation seeded from each member cluster's local pod.

    Each iteration uses a connection string seeded with that cluster's pod-0
    Service FQDN. Note: `?replicaSet=` triggers pymongo's RS topology discovery,
    so writes/aggregates land on the *primary* regardless of which seed was
    used — so this verifies that $search works end-to-end (mongot index ready,
    auth wired, Envoy serving) but does NOT prove per-cluster mongot isolation.
    Strict per-cluster routing would need `?directConnection=true&readPreference=
    secondaryPreferred`, which is out of scope here.
    """
    for mcc in member_cluster_clients:
        seed = _seed_pod_fqdn(mdb.name, namespace, mcc.cluster_index)
        conn = (
            f"mongodb://{USER_NAME}:{USER_PASSWORD}@{seed}:27017/"
            f"?replicaSet={mdb.name}"
        )
        tester = SearchTester(conn, use_ssl=True, ca_path=issuer_ca_filepath)
        logger.info(f"Running $search seeded from cluster {mcc.cluster_name} (pod {seed})")
        verify_text_search_query(tester)
