"""MC-Sharded e2e: MongoDBSearch with SCRAM mTLS + gRPC mTLS (encrypted keys).

Based on q3_mc_sharded_external_mtls, adds:
- Encrypted private keys on per-shard gRPC server certs (tls.keyFilePassword)
- SCRAM client cert with encrypted private key for mongot->mongod mTLS
- Validates operator-managed secrets created on correct member clusters
"""

from typing import List

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
from kubetester.certs import create_tls_certs, generate_cert
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
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.common.search.tls_utils import create_keyfile_password_secret, encrypt_tls_key_with_password
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-sh-scram"
MDBS_RESOURCE_NAME = "mdb-mc-sh-scram-search"

SHARD_COUNT = 2
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

# Encrypted key passwords
GRPC_KEY_PASSWORD = "test-grpc-key-password"
# Dedicated secret holding the gRPC keyfile password (spec.security.tls.keyFilePasswordSecretRef)
GRPC_KEY_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-grpc-key-password"
SCRAM_CLIENT_CERT_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-scram-tls-user"
SCRAM_KEY_PASSWORD = "test-scram-key-password"
# Dedicated secret holding the scram keyfile password (spec.source.tls.keyFilePasswordSecretRef)
SCRAM_KEY_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-scram-key-password"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 60


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
    name = create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)
    for mcc in member_cluster_clients:
        create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME, api_client=mcc.api_client)
    return name


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDB:
    """MC sharded MongoDB source with TLS+SCRAM."""
    resource = MongoDB.from_yaml(
        yaml_fixture("search-q3-mc-sharded.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    resource["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)
    resource["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, CONFIG_SRV_PER_CLUSTER)
    resource["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MONGOS_PER_CLUSTER)
    resource["spec"]["shardCount"] = SHARD_COUNT

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
    resource["spec"]["shard"]["additionalMongodConfig"] = {"setParameter": base_search_set_parameter}

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
    """MongoDBSearch over external sharded source with SCRAM mTLS + encrypted gRPC keys."""
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    router_hosts = [
        f"{MDB_RESOURCE_NAME}-mongos-{ci}-{pi}-svc.{namespace}.svc.cluster.local:27017"
        for ci, n in enumerate(MONGOS_PER_CLUSTER)
        if n
        for pi in range(n)
    ]

    shards = [
        {
            "shardName": f"{MDB_RESOURCE_NAME}-{si}",
            "hosts": [
                f"{MDB_RESOURCE_NAME}-{si}-{ci}-0-svc.{namespace}.svc.cluster.local:27017"
                for ci in range(len(MEMBERS_PER_CLUSTER))
                if MEMBERS_PER_CLUSTER[ci] is not None
            ],
        }
        for si in range(SHARD_COUNT)
    ]

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {"name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password", "key": "password"},
        "external": {
            "shardedCluster": {"router": {"hosts": router_hosts}, "shards": shards},
            "tls": {"ca": {"name": ca_configmap}},
        },
        "tls": {
            "clientCertificateSecretRef": {"name": SCRAM_CLIENT_CERT_SECRET_NAME},
            "keyFilePasswordSecretRef": {"name": SCRAM_KEY_PASSWORD_SECRET_NAME},
        },
    }

    resource["spec"]["security"] = {
        "tls": {
            "certsSecretPrefix": MDBS_TLS_CERT_PREFIX,
            "keyFilePasswordSecretRef": {"name": GRPC_KEY_PASSWORD_SECRET_NAME},
        }
    }

    resource["spec"]["clusters"] = [
        {
            "name": mcc.cluster_name,
            "index": mcc.cluster_index,
            "replicas": MONGOT_REPLICAS_PER_CLUSTER,
            "loadBalancer": {
                "managed": {
                    "externalHostname": search_resource_names.shard_proxy_svc_hostname_template(
                        MDBS_RESOURCE_NAME, namespace, _idx(mcc)
                    ),
                    # Shard-agnostic cluster-level endpoint for mongos: the per-cluster proxy-svc FQDN
                    # (matches the LB cert SAN). Distinct per cluster via the cluster index.
                    "routerHostname": search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, _idx(mcc)),
                },
            },
        }
        for mcc in member_cluster_clients
    ]

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


def _build_user(yaml_filename, name, username, namespace, central_cluster_client):
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def admin_user(namespace, central_cluster_client):
    return _build_user(
        "mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, ADMIN_USER_NAME, namespace, central_cluster_client
    )


@fixture(scope="module")
def user(namespace, central_cluster_client):
    return _build_user("mongodbuser-mdb-user.yaml", USER_NAME, USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mongot_user(namespace, central_cluster_client):
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
    )


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_ca(ca_configmap: str):
    assert ca_configmap == CA_CONFIGMAP_NAME


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_install_source_tls_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb: MongoDB,
):
    """Source MongoDB per-component TLS certs."""

    def _issue(component_resource: str, secret_name: str, distribution):
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
            f"{MDB_RESOURCE_NAME}-{shard_idx}",
            f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-{shard_idx}-cert",
            MEMBERS_PER_CLUSTER,
        )
    _issue(
        f"{MDB_RESOURCE_NAME}-config", f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-config-cert", CONFIG_SRV_PER_CLUSTER
    )
    _issue(f"{MDB_RESOURCE_NAME}-mongos", f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-mongos-cert", MONGOS_PER_CLUSTER)


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_mdb_source(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_user_credentials(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    def _apply(u, password):
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


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_search_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Per-(cluster, shard) mongot gRPC certs with encrypted keys + LB certs."""
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
        # Encrypt each per-shard cert's private key
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            secret_name = search_resource_names.shard_tls_cert_name(
                MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX, cluster_index=i
            )
            encrypt_tls_key_with_password(namespace, secret_name, GRPC_KEY_PASSWORD)
        logger.info(f"Per-shard gRPC certs encrypted for cluster_index={i}")

    # One gRPC keyfile password secret shared by every per-shard cert.
    create_keyfile_password_secret(namespace, GRPC_KEY_PASSWORD_SECRET_NAME, GRPC_KEY_PASSWORD)

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


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_scram_client_certificate(namespace: str, multi_cluster_issuer: str):
    """SCRAM client cert for mongot mTLS to mongod, with encrypted key."""
    generate_cert(
        namespace=namespace,
        pod="",
        dns="",
        issuer=multi_cluster_issuer,
        spec={"usages": ["digital signature", "key encipherment", "client auth"], "dnsNames": ["scram-client"]},
        secret_name=SCRAM_CLIENT_CERT_SECRET_NAME,
    )
    encrypt_tls_key_with_password(namespace, SCRAM_CLIENT_CERT_SECRET_NAME, SCRAM_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, SCRAM_KEY_PASSWORD_SECRET_NAME, SCRAM_KEY_PASSWORD)
    logger.info(f"SCRAM client cert {SCRAM_CLIENT_CERT_SECRET_NAME} created and encrypted")


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_replicate_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Copy centrally-issued Secrets to each member cluster."""
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, mcc: MultiClusterClient) -> None:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)

    # Shared secrets — same copy to every member cluster.
    for secret_name in [
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
        SCRAM_CLIENT_CERT_SECRET_NAME,
        GRPC_KEY_PASSWORD_SECRET_NAME,
        SCRAM_KEY_PASSWORD_SECRET_NAME,
    ]:
        for mcc in member_cluster_clients:
            _copy(secret_name, mcc)
        logger.info(f"Replicated {secret_name}")

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
        logger.info(f"Replicated per-cluster Secrets to {mcc.cluster_name}")


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_search_cr(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_per_cluster_resources_exist(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every (cluster, shard) pair must have a mongot StatefulSet on the member cluster."""
    for i, mcc in enumerate(member_cluster_clients):
        for shard_idx in range(SHARD_COUNT):
            shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
            sts_name = search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name, i)
            mcc.read_namespaced_stateful_set(sts_name, namespace)
        logger.info(f"[{mcc.cluster_name}] all per-shard StatefulSets verified")


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_envoy_deployments_ready(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    for i, mcc in enumerate(member_cluster_clients):
        deploy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, cluster_index=i)
        apps = mcc.apps_v1_api()
        assert_deployment_ready_in_cluster(apps, name=deploy_name, namespace=namespace)
        logger.info(f"[{mcc.cluster_name}] Envoy {deploy_name} Ready")


def _mongos_search_tester(namespace: str, cluster_index: int) -> SearchTester:
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-{cluster_index}-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{USER_NAME}:{USER_PASSWORD}@{mongos_host}/?directConnection=true&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


def _admin_search_tester(namespace: str) -> SearchTester:
    mongos_host = f"{MDB_RESOURCE_NAME}-mongos-0-0-svc.{namespace}.svc.cluster.local:27017"
    conn_str = (
        f"mongodb://{ADMIN_USER_NAME}:{ADMIN_USER_PASSWORD}@{mongos_host}/?directConnection=true&authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_restore_sample_database(namespace: str, tools_pod):
    tester = _admin_search_tester(namespace)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_shard_sample_collection(namespace: str):
    """Shard and redistribute the sample collection.

    reshardCollection is a long-running op that may fail if the automation agent
    restarts mongos mid-operation. Retry with a fresh connection if that happens.
    """

    def _shard() -> tuple:
        try:
            admin = _admin_search_tester(namespace)
            admin.shard_and_distribute_collection("sample_mflix", "movies")
            return True, "Collection sharded and distributed"
        except (pymongo.errors.AutoReconnect, pymongo.errors.ConnectionFailure) as exc:
            logger.warning(f"Connection lost during sharding, will retry: {exc}")
            return False, f"Connection error: {exc}"

    run_periodically(_shard, timeout=900, sleep_time=30, msg="shard_and_distribute_collection")


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_create_search_index(namespace: str):
    tester = _mongos_search_tester(namespace, 0)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_execute_search_query(namespace: str):
    """$search end-to-end proves SCRAM mTLS + encrypted gRPC keys work in MC sharded."""
    tester = _mongos_search_tester(namespace, 0)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search() -> tuple:
        try:
            results = movies.text_search_movies("Star Wars")
            if len(results) > 0:
                return True, f"$search returned {len(results)} results"
            return False, "$search returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$search error: {exc}"

    run_periodically(execute_search, timeout=SEARCH_QUERY_RETRY_TIMEOUT, sleep_time=5, msg="$search via mongos-0")


@mark.e2e_search_ext_mc_sharded_scram_enc_mtls
def test_per_cluster_search_query(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    """$search via each cluster's mongos -- proves both clusters' mTLS paths work."""
    for cluster_index in range(len(member_cluster_clients)):
        tester = _mongos_search_tester(namespace, cluster_index)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        def execute_search(_idx=cluster_index) -> tuple:
            try:
                results = movies.text_search_movies("Star Wars")
                if len(results) > 0:
                    return True, f"cluster {_idx}: $search returned {len(results)} results"
                return False, f"cluster {_idx}: no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster {_idx}: {exc}"

        run_periodically(
            execute_search, timeout=SEARCH_QUERY_RETRY_TIMEOUT, sleep_time=5, msg=f"cluster {cluster_index}"
        )
