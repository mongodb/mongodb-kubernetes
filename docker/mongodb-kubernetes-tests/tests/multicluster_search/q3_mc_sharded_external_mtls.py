"""Q3-MC-Sharded e2e: 2-cluster MongoDBSearch backed by an external sharded MongoDB (MultiCluster).

Exercises per-cluster × per-shard fan-out: each (cluster, shard) pair gets its own mongot
StatefulSet, Service, ConfigMap, and proxy Service; each cluster also gets a cluster-level
proxy Service (for mongos) and an Envoy Deployment.
"""

from typing import List

import kubernetes
import pymongo.errors
import yaml
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
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
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.connectivity import CLUSTER_LOCATION_TAG_KEY
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-sh-ext-lb"
MDBS_RESOURCE_NAME = "mdb-mc-sh-ext-lb-search"

SHARD_COUNT = 3
# 2 member clusters, 1 mongod per shard per cluster
MEMBERS_PER_CLUSTER: List[int | None] = [1, 1]
MONGOS_PER_CLUSTER: List[int | None] = [1, 1]
CONFIG_SRV_PER_CLUSTER: List[int | None] = [1, 1]

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_PROXY_PORT = 27028

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
SOURCE_CERT_PREFIX = "clustercert"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 60


# =============================================================================
# Fixtures
# =============================================================================


def _idx(mcc: MultiClusterClient) -> int:
    """Narrow ``mcc.cluster_index`` (Optional[int]) to int for the resource-name helpers."""
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


@fixture(scope="module")
def ca_configmap(
    issuer_ca_filepath: str,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> str:
    # Central cluster first (operators reads from here), then each member cluster
    # so mongot pods can verify the source MongoDB TLS cert.
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
    """3-shard MC sharded MongoDB source with TLS+SCRAM, distributed across 2 member clusters."""
    resource = MongoDB.from_yaml(
        yaml_fixture("search-q3-mc-sharded.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

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

    # MC MongoDB has no per-cluster additionalMongodConfig, so both mongos pods get
    # the same value; cluster-0's Envoy is namespace-scoped and reachable from cluster-1.
    cluster_idx = 0
    cluster_level_endpoint = (
        f"{search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, cluster_idx)}" f":{ENVOY_PROXY_PORT}"
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
    # Non-routing search params on spec.shard cover any shard that lacks an override;
    # shardOverrides take precedence for mongotHost / searchIndexManagementHostAndPort.
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
def mdbs(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDB,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch over an external sharded source, MC topology.

    externalHostname starts with {shardName}. so the operator can derive the
    cluster-level proxy-svc FQDN for mongos by stripping that prefix.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    # Per-pod headless Services (`<sts>-<clusterIdx>-<podIdx>-svc`) are reachable
    # cross-cluster via Istio; the per-cluster `<sts>-<clusterIdx>-svc` is not.
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
                for cluster_idx in range(len(MEMBERS_PER_CLUSTER))
                if MEMBERS_PER_CLUSTER[cluster_idx] is not None
            ],
        }
        for shard_idx in range(SHARD_COUNT)
    ]

    resource["spec"]["source"] = {
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

    resource["spec"]["security"] = {
        "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
    }

    clusters = []
    for mcc in member_cluster_clients:
        assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
        clusters.append(
            {
                "name": mcc.cluster_name,
                "index": mcc.cluster_index,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "loadBalancer": {
                    "managed": {
                        "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                            MDBS_RESOURCE_NAME, namespace, _idx(mcc)
                        ),
                        # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc
                        # FQDN (matches the LB cert SAN). Distinct per cluster via the cluster index.
                        "routerHostname": search_resource_names.mc_proxy_svc_fqdn(
                            MDBS_RESOURCE_NAME, namespace, _idx(mcc)
                        ),
                    },
                },
                "syncSourceSelector": {"matchTagSets": [{CLUSTER_LOCATION_TAG_KEY: mcc.cluster_name}]},
            }
        )
    resource["spec"]["clusters"] = clusters

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, ADMIN_USER_NAME, namespace, central_cluster_client
    )


@fixture(scope="module")
def user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
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


# =============================================================================
# Test steps
# =============================================================================


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_ca(ca_configmap: str):
    assert ca_configmap == CA_CONFIGMAP_NAME


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
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


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_mdb_source(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_user_credentials(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    def _apply(u: MongoDBUser, password: str):
        create_or_update_secret(
            namespace,
            name=u["spec"]["passwordSecretKeyRef"]["name"],
            data={"password": password},
            api_client=central_cluster_client,
        )
        u.update()

    _apply(admin_user, ADMIN_USER_PASSWORD)
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)
    _apply(user, USER_PASSWORD)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    _apply(mongot_user, MONGOT_USER_PASSWORD)


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Issue per-(cluster, shard) mongot certs + LB certs on central.

    cert-manager is installed only on the central cluster (`tests/conftest.py::cert_manager`).
    Issuing per-cluster Certificate CRs on members would 404. Secrets land on central; the
    next step (`test_replicate_secrets_to_members`) copies them to each member.
    """
    for i in range(len(member_cluster_clients)):
        create_per_shard_search_tls_certs(
            namespace=namespace,
            issuer=multi_cluster_issuer,
            prefix=MDBS_TLS_CERT_PREFIX,
            shard_count=SHARD_COUNT,
            mdb_resource_name=MDB_RESOURCE_NAME,
            mdbs_resource_name=MDBS_RESOURCE_NAME,
            cluster_index=i,
            api_client=central_cluster_client,
        )
        logger.info(f"Per-shard Search TLS certs created on central for cluster_index={i}")

    # Single LB cert with SANs covering every cluster's proxy Services — secret name
    # has no cluster index, so per-cluster issuance would overwrite the same Secret.
    create_lb_certificates(
        namespace=namespace,
        issuer=multi_cluster_issuer,
        shard_count=SHARD_COUNT,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        cluster_indexes=list(range(len(member_cluster_clients))),
        api_client=central_cluster_client,
    )
    logger.info(f"LB certs created on central with SANs for cluster_indexes={list(range(len(member_cluster_clients)))}")


@mark.e2e_search_q3_mc_sharded_external_mtls
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
    logger.info(f"Replicated mongot password to {len(member_cluster_clients)} member(s)")

    # Per-cluster Secrets — LB certs + per-shard mongot certs go to their owning cluster.
    for i, mcc in enumerate(member_cluster_clients):
        _copy(search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, i), mcc)
        _copy(search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, i), mcc)
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            _copy(
                search_resource_names.shard_tls_cert_name(
                    MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=i
                ),
                mcc,
            )
        logger.info(f"Replicated per-cluster Secrets to {mcc.cluster_name} (cluster_index={i})")


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_search_cr(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_per_cluster_mongot_resources_exist(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every (cluster, shard) pair must have a mongot StatefulSet, headless Service, and ConfigMap
    on the owning member cluster's API server, and each shard's mongot config must carry that
    cluster's matchTagSets as syncSource.replicationReader.tagSets.
    """
    for i, mcc in enumerate(member_cluster_clients):
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, i)
            svc_name = search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name, i)
            cm_name = search_resource_names.shard_configmap_name(MDBS_RESOURCE_NAME, shard_name, i)
            proxy_svc_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name, i)

            mcc.read_namespaced_stateful_set(sts_name, namespace)
            mcc.read_namespaced_service(svc_name, namespace)
            mcc.read_namespaced_service(proxy_svc_name, namespace)
            cm = mcc.read_namespaced_config_map(cm_name, namespace)

            # matchTagSets -> replicationReader.tagSets. Sharded sync source is the router, but the
            # tag set renders the same way as RS; config-render check is image-independent.
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
            logger.info(
                f"[{mcc.cluster_name}] shard {shard_name}: STS/svc/proxy-svc/cm + tagSets verified (cluster_index={i})"
            )


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_cluster_level_proxy_service_per_cluster(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each cluster must expose a shard-agnostic cluster-level proxy Service for mongos routing.

    This Service is the M3 addition: single-cluster sharded used shard-0 proxy before; now
    every sharded deployment gets a dedicated cluster-level Service.
    """
    for i, mcc in enumerate(member_cluster_clients):
        svc_name = search_resource_names.mc_proxy_svc_name(MDBS_RESOURCE_NAME, i)
        svc = mcc.read_namespaced_service(svc_name, namespace)
        labels = svc.metadata.labels or {}
        assert labels.get("component") == "search-proxy", (
            f"[{mcc.cluster_name}] cluster-level proxy Service {svc_name} missing "
            f"component=search-proxy label; got {labels}"
        )
        logger.info(f"[{mcc.cluster_name}] cluster-level proxy Service {svc_name} exists and labelled correctly")


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_envoy_deployment_per_cluster(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    for i, mcc in enumerate(member_cluster_clients):
        deploy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, cluster_index=i)
        apps = mcc.apps_v1_api()
        assert_deployment_ready_in_cluster(apps, name=deploy_name, namespace=namespace)
        logger.info(f"[{mcc.cluster_name}] Envoy Deployment {deploy_name} Ready (cluster_index={i})")


# =============================================================================
# Data plane: $search via per-cluster mongos
# =============================================================================


def _per_cluster_mongos_search_tester(
    namespace: str,
    cluster_index: int,
    username: str,
    password: str,
) -> SearchTester:
    """SearchTester pinned to a specific cluster's mongos via its per-pod headless Service.

    `directConnection=true` keeps the driver from discovering the other cluster's mongos.
    """
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{mongos_host}/" f"?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_restore_sample_database(namespace: str, tools_pod):
    tester = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_shard_sample_collection(namespace: str):
    """Shard sample_mflix.movies across the 3 shards so $search exercises every per-shard mongot."""
    admin = _per_cluster_mongos_search_tester(namespace, 0, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    admin.shard_and_distribute_collection("sample_mflix", "movies")


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_create_search_index(namespace: str):
    tester = _per_cluster_mongos_search_tester(namespace, 0, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_execute_text_search_query(namespace: str):
    """End-to-end $search against the first cluster's mongos — quick smoke before per-cluster."""
    tester = _per_cluster_mongos_search_tester(namespace, 0, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search() -> tuple:
        try:
            results = movies.text_search_movies("Star Wars")
            count = len(results)
            if count > 0:
                return True, f"$search returned {count} results"
            return False, "$search returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$search error: {exc}"

    run_periodically(execute_search, timeout=SEARCH_QUERY_RETRY_TIMEOUT, sleep_time=5, msg="$search via mongos-0")


@mark.e2e_search_q3_mc_sharded_external_mtls
def test_per_cluster_search_query(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """$search via each cluster's mongos — proves both clusters' Envoy + per-shard mongot
    paths return results. Mongos→mongot uses the cluster-local proxy Service (M3), so a
    per-cluster non-empty result is what validates the (cluster × shard) data path.
    """
    for cluster_index in range(len(member_cluster_clients)):
        tester = _per_cluster_mongos_search_tester(namespace, cluster_index, USER_NAME, USER_PASSWORD)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        def execute_search(_idx: int = cluster_index) -> tuple:
            try:
                results = movies.text_search_movies("Star Wars")
                count = len(results)
                if count > 0:
                    titles = [r.get("title") for r in results[:5]]
                    logger.info(f"cluster {_idx}: $search returned {count} results: {titles}")
                    return True, f"cluster {_idx}: $search returned {count} results"
                return False, f"cluster {_idx}: $search returned no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster {_idx}: $search error: {exc}"

        run_periodically(
            execute_search,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            sleep_time=5,
            msg=f"cluster {cluster_index}: $search via mongos",
        )
