"""MC-RS e2e: MongoDBSearch with X509 mTLS + gRPC mTLS (encrypted keys).

Based on q2_mc_rs_steady pattern (external MongoDBMulti RS source), adds:
- Encrypted private keys on mongot gRPC server cert (tls.keyFilePassword)
- X509 client cert with encrypted private key for mongot->mongod auth
- Validates operator-managed secrets created on correct member clusters
"""

from typing import List

import kubernetes
import pymongo.errors
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
from kubetester.certs import create_tls_certs, generate_cert
from kubetester.certs_mongodb_multi import (
    create_multi_cluster_mongodb_x509_tls_certs,
    create_multi_cluster_x509_agent_certs,
)
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
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.common.search.tls_utils import create_keyfile_password_secret, encrypt_tls_key_with_password
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-rs-x509"
MDBS_RESOURCE_NAME = "mdb-mc-rs-x509-search"

MEMBERS_PER_CLUSTER: List[int | None] = [2, 2]
MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 2
ENVOY_PROXY_PORT = 27028

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
SOURCE_CERT_PREFIX = "clustercert"
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"

# Encrypted key passwords
GRPC_KEY_PASSWORD = "test-grpc-key-password"
# Dedicated secret holding the gRPC keyfile password (spec.security.tls.keyFilePasswordSecretRef)
GRPC_KEY_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-grpc-key-password"
X509_CLIENT_CERT_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-x509-sync-client-cert"
X509_CLIENT_CERT_CN = "mongot-sync-source"
X509_AUTH_KEY_PASSWORD = "test-x509-key-password"
# Dedicated secret holding the x509 keyfile password (spec.source.x509.keyFilePasswordSecretRef)
X509_KEY_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-x509-key-password"

SEARCH_INDEX_READY_TIMEOUT = 300
SEARCH_QUERY_RETRY_TIMEOUT = 60


def _idx(mcc: MultiClusterClient) -> int:
    """Narrow ``mcc.cluster_index`` (Optional[int]) to int for the resource-name helpers."""
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def get_x509_subject_dn(namespace: str) -> str:
    return f"CN={X509_CLIENT_CERT_CN},OU={namespace},O=cluster.local-client,L=NY,ST=NY,C=US"


@fixture(scope="module")
def helper(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> MCSearchDeploymentHelper:
    return MCSearchDeploymentHelper(
        namespace=namespace,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients},
    )


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS + X509+SCRAM auth."""
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
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
        "authentication": {
            "enabled": True,
            "modes": ["X509", "SCRAM"],
            "agents": {"mode": "X509"},
            "internalCluster": "X509",
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDBMulti,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch with X509 auth + encrypted gRPC key, MC RS topology."""
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    seeds = [f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names()]

    resource["spec"]["source"] = {
        "external": {
            "hostAndPorts": seeds,
            "tls": {"ca": {"name": ca_configmap}},
        },
        "x509": {
            "clientCertificateSecretRef": {"name": X509_CLIENT_CERT_SECRET_NAME},
            "keyFilePasswordSecretRef": {"name": X509_KEY_PASSWORD_SECRET_NAME},
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
                    "externalHostname": search_resource_names.mc_proxy_svc_fqdn(
                        MDBS_RESOURCE_NAME, namespace, _idx(mcc)
                    ),
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
def x509_mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    """X509 user for mongot sync source auth in $external."""
    user_dn = get_x509_subject_dn(namespace)
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{MDBS_RESOURCE_NAME}-mongot-x509-user",
    )
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = user_dn
        resource["spec"]["db"] = "$external"
        resource["spec"].pop("passwordSecretKeyRef", None)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_install_source_tls_certificates(
    multi_cluster_issuer: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
):
    """Source MongoDB TLS bundle + agent/clusterfile certs for x509 (multi-cluster aware)."""
    create_multi_cluster_mongodb_x509_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        member_cluster_clients,
        central_cluster_client,
        mdb,
    )
    create_multi_cluster_x509_agent_certs(
        multi_cluster_issuer,
        f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-agent-certs",
        central_cluster_client,
        mdb,
    )
    create_multi_cluster_mongodb_x509_tls_certs(
        multi_cluster_issuer,
        f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-clusterfile",
        member_cluster_clients,
        central_cluster_client,
        mdb,
    )


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_mdb_resource(mdb: MongoDBMulti):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_user_credentials(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    x509_mongot_user: MongoDBUser,
):
    create_or_update_secret(
        namespace,
        name=admin_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": ADMIN_USER_PASSWORD},
        api_client=central_cluster_client,
    )
    admin_user.update()
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    create_or_update_secret(
        namespace,
        name=user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": USER_PASSWORD},
        api_client=central_cluster_client,
    )
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    x509_mongot_user.update()
    x509_mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_deploy_lb_certificates(namespace: str, multi_cluster_issuer: str, helper: MCSearchDeploymentHelper):
    """Managed LB server + client certs, one per cluster."""
    server_domains = [
        f"{MDBS_RESOURCE_NAME}-search-{helper.cluster_index(name)}-proxy-svc.{namespace}.svc.cluster.local"
        for name in helper.member_cluster_names()
    ]

    for name in helper.member_cluster_names():
        ci = helper.cluster_index(name)
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
        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=f"{deployment_name}-client",
            replicas=1,
            service_name=deployment_name,
            additional_domains=[f"*.{namespace}.svc.cluster.local"],
            secret_name=lb_client_cert_name,
        )


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_search_tls_certificate(namespace: str, multi_cluster_issuer: str, helper: MCSearchDeploymentHelper):
    """mongot gRPC server cert with encrypted private key."""
    secret_name = search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    additional_domains: List[str] = []
    for name in helper.member_cluster_names():
        idx = helper.cluster_index(name)
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-svc.{namespace}.svc.cluster.local")
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc.{namespace}.svc.cluster.local")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    encrypt_tls_key_with_password(namespace, secret_name, GRPC_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, GRPC_KEY_PASSWORD_SECRET_NAME, GRPC_KEY_PASSWORD)
    logger.info(f"gRPC server cert {secret_name} encrypted with password")


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_x509_client_certificate(namespace: str, multi_cluster_issuer: str):
    """X509 client cert for mongot auth to mongod, with encrypted key."""
    x509_spec = {
        "subject": {
            "countries": ["US"],
            "provinces": ["NY"],
            "localities": ["NY"],
            "organizations": ["cluster.local-client"],
            "organizationalUnits": [namespace],
        },
        "commonName": X509_CLIENT_CERT_CN,
        "usages": ["digital signature", "key encipherment", "client auth"],
        "dnsNames": [X509_CLIENT_CERT_CN],
    }
    generate_cert(
        namespace=namespace,
        pod="",
        dns="",
        issuer=multi_cluster_issuer,
        spec=x509_spec,
        secret_name=X509_CLIENT_CERT_SECRET_NAME,
    )
    encrypt_tls_key_with_password(namespace, X509_CLIENT_CERT_SECRET_NAME, X509_AUTH_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, X509_KEY_PASSWORD_SECRET_NAME, X509_AUTH_KEY_PASSWORD)
    logger.info(f"X509 client cert {X509_CLIENT_CERT_SECRET_NAME} created and encrypted")


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_replicate_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Replicate TLS Secrets, CA, and x509 client cert to every member cluster."""
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        source = central_core.read_namespaced_secret(name=secret_name, namespace=namespace)
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in targets:
            create_or_update_secret(
                namespace, secret_name, data, type=source.type or "Opaque", api_client=mcc.api_client
            )

    # Shared Secrets — same copy to every member cluster.
    for secret_name in [
        search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        X509_CLIENT_CERT_SECRET_NAME,
        GRPC_KEY_PASSWORD_SECRET_NAME,
        X509_KEY_PASSWORD_SECRET_NAME,
    ]:
        _copy(secret_name, member_cluster_clients)
        logger.info(f"replicated Secret {secret_name}")

    # Per-cluster LB certs — each member only needs the cert matching its own Envoy.
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        for secret_name in (
            search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci),
            search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci),
        ):
            _copy(secret_name, [mcc])
            logger.info(f"replicated per-cluster Secret {secret_name} into cluster {mcc.cluster_name}")

    source_cm = central_core.read_namespaced_config_map(name=CA_CONFIGMAP_NAME, namespace=namespace)
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, dict(source_cm.data or {}), api_client=mcc.api_client)


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_search_resource(mdbs: MongoDBSearch):
    """MongoDBSearch must reach Running -- validates operator-managed secrets on members."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_verify_per_cluster_resources(
    namespace: str, helper: MCSearchDeploymentHelper, member_cluster_clients: List[MultiClusterClient]
):
    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        mcc.read_namespaced_stateful_set(f"{MDBS_RESOURCE_NAME}-search-{idx}", namespace)
        mcc.read_namespaced_service(f"{MDBS_RESOURCE_NAME}-search-{idx}-svc", namespace)
        mcc.read_namespaced_service(f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc", namespace)
        deploy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, cluster_index=idx)
        assert_deployment_ready_in_cluster(mcc.apps_v1_api(), name=deploy_name, namespace=namespace)
        logger.info(f"[{mcc.cluster_name}] per-cluster resources + Envoy verified")


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_patch_per_cluster_mongot_host(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    namespace: str,
):
    """Patch OM automation config to set mongotHost per process pointing to envoy proxy."""
    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    for process in ac.get("processes", []):
        hostname = process.get("hostname", "")
        # Determine cluster index from hostname pattern: mdb-mc-rs-x509-{clusterIdx}-{podIdx}-svc...
        parts = hostname.split("-svc")[0].split("-")
        # The cluster index is after the resource name parts
        # hostname pattern: mdb-mc-rs-x509-{clusterIdx}-{podIdx}-svc.namespace...
        cluster_idx = int(parts[-2])

        proxy_host = (
            f"{MDBS_RESOURCE_NAME}-search-{cluster_idx}-proxy-svc.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"
        )
        set_param = process.setdefault("args2_6", {}).setdefault("setParameter", {})
        set_param["mongotHost"] = proxy_host
        set_param["searchIndexManagementHostAndPort"] = proxy_host
        logger.info(f"Patched process {process.get('name')} mongotHost -> {proxy_host}")

    ac["version"] = ac.get("version", 0) + 1
    om_tester.om_request("put", ac_path, json_object=ac)
    om_tester.wait_agents_ready(timeout=900)
    logger.info("All agents reached goal state with mongotHost configured")


def _search_tester(mdb: MongoDBMulti, username: str, password: str) -> SearchTester:
    seed_host = f"{mdb.name}-0-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={mdb.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_restore_sample_database(mdb: MongoDBMulti, tools_pod):
    tester = _search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_create_search_index(mdb: MongoDBMulti):
    tester = _search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_ext_mc_rs_x509_enc_mtls
def test_execute_search_query(mdb: MongoDBMulti):
    """$search end-to-end proves X509 mTLS + encrypted gRPC keys work in MC RS."""
    tester = _search_tester(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search() -> tuple:
        try:
            results = movies.text_search_movies("Star Wars")
            if len(results) > 0:
                return True, f"$search returned {len(results)} results"
            return False, "$search returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$search error: {exc}"

    run_periodically(execute_search, timeout=SEARCH_QUERY_RETRY_TIMEOUT, sleep_time=5, msg="$search query")
