"""Simulated multi-cluster MongoDBSearch e2e: cross-cluster RS source, 1 central + 2 simulated-MC operators.

Uses only the first two of the three harness member clusters; that is why every
member_cluster_clients[:2] / member_cluster_names[:2] slice appears in this module.
"""

from __future__ import annotations

import os
from typing import List, Tuple

import kubernetes
import pytest
from kubetester import try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.multicluster.multicluster_utils import assert_workload_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.mc_search_helper import (
    _assert_mongot_host_on_disk,
    assert_mongot_sync_source_hosts,
    patch_mongot_host_via_ac,
    replicate_search_secrets_to_members,
    verify_per_cluster_envoy_sni,
)
from tests.common.search.movies_search_helper import (
    EMBEDDING_QUERY_KEY_ENV_VAR,
    EmbeddedMoviesSearchHelper,
    SampleMoviesSearchHelper,
)
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca, per_cluster_search_tester
from tests.conftest import get_issuer_ca_filepath
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

MDB_RESOURCE_NAME = "mdb-mc-sim"
MDBS_RESOURCE_NAME = "mdb-mc-sim-search"

MEMBERS_PER_CLUSTER: List[int | None] = [3, 3]
MONGOT_REPLICAS_PER_CLUSTER = 3

# Generous index-ready timeout to account for cross-cluster mesh latency during sync.
SEARCH_INDEX_READY_TIMEOUT = 300
# mongot may still be in INITIAL_SYNC briefly after the index reports READY.
SEARCH_QUERY_RETRY_TIMEOUT = 90

# create_issuer_ca writes a ConfigMap (keys ca-pem/ca.crt/mms-ca.crt) consumed as the CA by
# both MongoDBMulti.spec.security.tls.ca and MongoDBSearch source.external.tls.ca — never a Secret.
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
# Must match the operator's cert-secret convention `{certsSecretPrefix}-{name}-cert`
# (see certsSecretPrefix below); otherwise the operator can't find the TLS bundle.
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"


@fixture(scope="module")
def central_mc_operator(
    namespace: str,
    central_cluster_name: str,
    multi_cluster_operator_installation_config: dict,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
) -> Operator:
    """Central MC operator (watch_search=False); uses only the first two harness member clusters."""
    return install_central_mc_operator(
        namespace,
        central_cluster_name,
        multi_cluster_operator_installation_config,
        central_cluster_client,
        member_cluster_clients[:2],
        member_cluster_names[:2],
        watch_search=False,
    )


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    ca_configmap: str,
) -> MongoDBMulti:
    """6-member cross-cluster ReplicaSet (3+3) with TLS+SCRAM; applied only to the central cluster."""
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
def per_cluster_mdbs_search(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    ca_configmap: str,
) -> List[Tuple[MultiClusterClient, MongoDBSearch]]:
    """Same CR (spec.clusters lists all clusters) applied to each member's API; each operator projects to its own slice."""
    member_cluster_clients = member_cluster_clients[:2]

    # Seed list: every cross-cluster RS member FQDN (Istio MC east-west routing
    # makes every cluster's DNS resolve every other cluster's mongod Service).
    seeds = [
        f"{MDB_RESOURCE_NAME}-{c}-{m}-svc.{namespace}.svc.cluster.local:27017"
        for c, members in enumerate(MEMBERS_PER_CLUSTER[: len(member_cluster_clients)])
        if members
        for m in range(members)
    ]

    return build_per_cluster_search_crs(
        namespace,
        member_cluster_clients,
        MDBS_RESOURCE_NAME,
        MONGOT_REPLICAS_PER_CLUSTER,
        # Per-cluster literal: the cluster's own proxy-svc FQDN (distinct per clusterIndex),
        # mirroring q2_mc_rs_steady.
        lambda idx: search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, idx),
        {
            "hostAndPorts": seeds,
            "tls": {"ca": {"name": ca_configmap}},
        },
    )


@mark.e2e_search_simulated_mc_rs
def test_install_central_mc_operator(central_mc_operator: Operator):
    central_mc_operator.wait_for_operator_ready()


@mark.e2e_search_simulated_mc_rs
def test_install_simulated_operators_per_member(
    namespace: str,
    central_mc_operator: Operator,
    multi_cluster_operator_installation_config: dict,
    member_cluster_clients: List[MultiClusterClient],
):
    install_simulated_operators_per_member(
        namespace, multi_cluster_operator_installation_config, member_cluster_clients
    )


@mark.e2e_search_simulated_mc_rs
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


@mark.e2e_search_simulated_mc_rs
def test_mongodb_running(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_simulated_mc_rs
def test_create_users(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    mdb_user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    create_search_users(namespace, central_cluster_client, admin_user, mdb_user, mongot_user)


@mark.e2e_search_simulated_mc_rs
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    member_cluster_clients: List[MultiClusterClient],
):
    member_cluster_clients = member_cluster_clients[:2]
    server_domains = [
        search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, _idx(mcc))
        for mcc in member_cluster_clients
    ]

    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        deployment_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci)
        lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci)

        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=deployment_name,
            replicas=ENVOY_LB_REPLICAS,
            service_name=deployment_name,
            additional_domains=server_domains,
            secret_name=lb_server_cert_name,
        )
        logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=f"{deployment_name}-client",
            replicas=1,
            service_name=deployment_name,
            additional_domains=[f"*.{namespace}.svc.cluster.local"],
            secret_name=lb_client_cert_name,
        )
        logger.info(f"LB client certificate created: {lb_client_cert_name}")


@mark.e2e_search_simulated_mc_rs
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
        additional_domains.append(
            f"{search_resource_names.mongot_service_name_for_cluster(MDBS_RESOURCE_NAME, idx)}.{namespace}.svc.cluster.local"
        )
        additional_domains.append(search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, idx))

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"mongot TLS certificate created with SANs={additional_domains}: {secret_name}")


@mark.e2e_search_simulated_mc_rs
def test_replicate_tls_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Simulated-MC operators read only their own etcd — TLS material must be present locally."""
    replicate_search_secrets_to_members(
        namespace=namespace,
        central_cluster_client=central_cluster_client,
        member_cluster_clients=member_cluster_clients[:2],
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mdbs_tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        mongot_user_name=MONGOT_USER_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@mark.e2e_search_simulated_mc_rs
def test_search_running_per_cluster(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    apply_search_crs_and_assert_running(per_cluster_mdbs_search)


def _source_seed_hosts(namespace: str) -> List[str]:
    """Source seed hosts each cluster's mongot syncs from; mirrors per_cluster_mdbs_search."""
    return [
        f"{MDB_RESOURCE_NAME}-{c}-{m}-svc.{namespace}.svc.cluster.local:27017"
        for c, members in enumerate(MEMBERS_PER_CLUSTER)
        if members
        for m in range(members)
    ]


@mark.e2e_search_simulated_mc_rs
def test_per_cluster_resources_exist(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_per_cluster_count(per_cluster_mdbs_search)
    seed_hosts = _source_seed_hosts(namespace)
    for mcc, mdbs in per_cluster_mdbs_search:
        idx = _idx(mcc)
        sts_name = search_resource_names.mongot_statefulset_name_for_cluster(MDBS_RESOURCE_NAME, idx)
        svc_name = search_resource_names.mongot_service_name_for_cluster(MDBS_RESOURCE_NAME, idx)
        proxy_svc_name = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, idx)
        mongot_cm_name = search_resource_names.mongot_configmap_name_for_cluster(MDBS_RESOURCE_NAME, idx)
        envoy_dep_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, idx)

        sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
        assert sts.spec.replicas == MONGOT_REPLICAS_PER_CLUSTER, (
            f"[{mcc.cluster_name}] {sts_name} expected {MONGOT_REPLICAS_PER_CLUSTER} replicas, "
            f"got {sts.spec.replicas}"
        )
        headless_svc = mcc.read_namespaced_service(svc_name, namespace)
        proxy_svc = mcc.read_namespaced_service(proxy_svc_name, namespace)

        # KUBE-154: even though the localized CR exists in this cluster (so a ref
        # WOULD be a valid GC target), the operator must not set ownerReferences on
        # any per-cluster write — lifecycle is owned by the label-based sweep.
        mongot_cm = mcc.read_namespaced_config_map(mongot_cm_name, namespace)
        tls_secret_name = f"{MDBS_RESOURCE_NAME}-search-certificate-key"
        tls_secret = mcc.core_v1_api().read_namespaced_secret(tls_secret_name, namespace)
        for obj, where in (
            (sts, f"STS {sts_name}"),
            (headless_svc, f"headless Service {svc_name}"),
            (proxy_svc, f"proxy Service {proxy_svc_name}"),
            (mongot_cm, f"mongot CM {mongot_cm_name}"),
            (tls_secret, f"operator TLS Secret {tls_secret_name}"),
        ):
            refs = obj.metadata.owner_references or []
            assert not refs, f"[{mcc.cluster_name}] {where}: unexpected ownerReferences {refs}"

        assert_workload_ready_in_cluster(mcc, namespace, {sts_name: MONGOT_REPLICAS_PER_CLUSTER}, envoy_dep_name)

        assert_mongot_sync_source_hosts(mcc, mongot_cm_name, namespace, seed_hosts)
        # status.loadBalancer is written by the Envoy controller on its own reconcile;
        # the Envoy Deployment being ready above means that write has happened.
        mdbs.assert_lb_status()
        logger.info(
            f"[{mcc.cluster_name}] sts={sts_name} (replicas={MONGOT_REPLICAS_PER_CLUSTER}) "
            f"svc={svc_name} proxy={proxy_svc_name} cm={mongot_cm_name} "
            f"envoy_lb={envoy_dep_name} (replicas={ENVOY_LB_REPLICAS})"
        )

    verify_per_cluster_envoy_sni(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients=[mcc for mcc, _ in per_cluster_mdbs_search],
        expected_upstreams_by_idx={
            _idx(mcc): [
                f"{search_resource_names.mongot_service_name_for_cluster(MDBS_RESOURCE_NAME, _idx(mcc))}"
                f".{namespace}.svc.cluster.local"
            ]
            for mcc, _ in per_cluster_mdbs_search
        },
    )


@mark.e2e_search_simulated_mc_rs
def test_status_per_cluster_local_only(
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    assert_status_running_local_only(per_cluster_mdbs_search)


def _expected_mongot_host_by_idx(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> dict:
    """Each entry is the cluster's own proxy svc FQDN:port — NOT a single shared cluster's —
    so a regression that funnels every mongod through one cluster's Envoy is caught.
    """
    return {
        _idx(mcc): (
            f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, _idx(mcc))}:{ENVOY_PROXY_PORT}"
        )
        for mcc in member_cluster_clients
    }


@mark.e2e_search_simulated_mc_rs
def test_patch_per_cluster_mongot_host(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """Set per-cluster mongotHost/searchIndexManagementHostAndPort directly on the OM
    automation config: MongoDBMultiCluster has no per-cluster additionalMongodConfig, and
    keeping these out of the CR spec stops later reconciles from clobbering the locality.
    """
    member_cluster_clients = member_cluster_clients[:2]
    expected_by_idx = _expected_mongot_host_by_idx(namespace, member_cluster_clients)
    logger.info(f"per-cluster mongotHost map: {expected_by_idx}")

    process_prefix = f"{mdb.name}-"

    def resolve_host(process_name: str) -> str | None:
        if not process_name.startswith(process_prefix):
            return None
        try:
            cluster_idx = int(process_name[len(process_prefix) :].split("-")[0])
        except ValueError:
            return None
        return expected_by_idx.get(cluster_idx)

    patch_mongot_host_via_ac(mdb, resolve_host)


@mark.e2e_search_simulated_mc_rs
def test_per_cluster_mongot_host_observed(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """On-disk read-back: each cluster's mongod must show ITS OWN proxy-svc FQDN, proving
    the agent applied the per-cluster locality (not just that OM REST accepted it).
    """
    member_cluster_clients = member_cluster_clients[:2]
    expected_by_idx = _expected_mongot_host_by_idx(namespace, member_cluster_clients)
    # MEMBERS_PER_CLUSTER is subscripted by the harness cluster_index (mcc.cluster_index), not list/loop position.
    _assert_mongot_host_on_disk(
        mdb,
        expected_by_idx,
        member_cluster_clients,
        members_for_idx=lambda idx: MEMBERS_PER_CLUSTER[idx] or 0,
    )


def _source_rs_search_tester(mdb: MongoDBMulti, username: str, password: str) -> SearchTester:
    """SearchTester pointed at the source MongoDBMulti RS via mesh DNS, using
    ?replicaSet=... so the driver discovers the primary for writes (restore + index).
    """
    seed_host = f"{mdb.name}-0-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={mdb.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_simulated_mc_rs
def test_restore_sample_database(mdb: MongoDBMulti, tools_pod):
    """mongorestore sample_mflix into the source RS once; both clusters' mongots sync from it."""
    tester = _source_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_simulated_mc_rs
def test_create_search_index(mdb: MongoDBMulti):
    """Create the $search index ONCE on the source RS; mongot replication propagates it to every per-cluster mongot."""
    tester = _source_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_rs
def test_create_vector_search_index(mdb: MongoDBMulti):
    """Create a $vectorSearch index on embedded_movies and wait for READY. Indexing needs
    no Voyage key, but it is gated by EMBEDDING_QUERY_KEY_ENV_VAR so the query test that
    follows always finds a READY index.
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")
    tester = _source_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.create_vector_search_index()
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_simulated_mc_rs
def test_per_cluster_search_query(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search direct-connected to EACH cluster's own mongod — proves every cluster's
    independent mongod -> local-Envoy -> local-mongot path returns rows, not just the
    cluster currently holding the primary.
    """
    member_cluster_clients = member_cluster_clients[:2]
    assert_per_cluster_count(member_cluster_clients)

    for mcc in member_cluster_clients:
        cluster_index = _idx(mcc)
        pod_host = f"{mdb.name}-{cluster_index}-0-svc.{mdb.namespace}.svc.cluster.local:27017"
        tester = per_cluster_search_tester(pod_host, USER_NAME, USER_PASSWORD)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        # Index is created once on the source RS; confirm THIS cluster's local mongot
        # has synced it queryable before asserting the deterministic result count.
        tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)
        # Deterministic compound query: the Hawaii/Alaska sample returns exactly 4 docs.
        movies.assert_search_query(retry_timeout=SEARCH_QUERY_RETRY_TIMEOUT)
        logger.info(f"cluster_index={cluster_index}: deterministic $search (==4) via local mongod OK")


@mark.e2e_search_simulated_mc_rs
def test_per_cluster_vector_search_query(
    namespace: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """$vectorSearch direct-connected to EACH cluster's own mongod (vector analogue of
    test_per_cluster_search_query). limit=4 against embedded_movies returns exactly 4 docs
    when the corpus has >=4 pre-embedded docs, so count==limit is invariant of ordering.
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")

    member_cluster_clients = member_cluster_clients[:2]
    assert_per_cluster_count(member_cluster_clients)

    limit = 4
    # Generate the query vector ONCE via the source RS; each cluster queries with the
    # same vector so results are comparable and the test is not flaky.
    seed_tester = _source_rs_search_tester(mdb, USER_NAME, USER_PASSWORD)
    query_vector = EmbeddedMoviesSearchHelper(search_tester=seed_tester).generate_query_vector("space exploration")

    for mcc in member_cluster_clients:
        cluster_index = _idx(mcc)
        pod_host = f"{mdb.name}-{cluster_index}-0-svc.{mdb.namespace}.svc.cluster.local:27017"
        tester = per_cluster_search_tester(pod_host, USER_NAME, USER_PASSWORD)
        embedded = EmbeddedMoviesSearchHelper(search_tester=tester)

        embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)

        doc_count = embedded.count_documents_with_embeddings()
        assert doc_count >= limit, (
            f"cluster_index={cluster_index}: only {doc_count} embedded_movies docs have "
            f"plot_embedding_voyage_3_large — need at least {limit} for a deterministic count==limit assertion"
        )

        embedded.assert_vector_search_count_eventually(
            query_vector,
            limit,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            msg_prefix=f"cluster {cluster_index}: ",
        )
        logger.info(f"cluster_index={cluster_index}: deterministic $vectorSearch (=={limit}) via local mongod OK")


@mark.e2e_search_simulated_mc_rs
def test_cross_cluster_isolation_absence(
    namespace: str,
    per_cluster_mdbs_search: List[Tuple[MultiClusterClient, MongoDBSearch]],
):
    """Each cluster must NOT contain the OTHER cluster's index-suffixed Search resources.
    Confirms each simulated operator only materialises its own LocalizeToCluster slice.
    """
    assert_cross_cluster_isolation(namespace, per_cluster_mdbs_search, MDBS_RESOURCE_NAME)
