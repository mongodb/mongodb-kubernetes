import tempfile

from kubernetes import client as k8s_client
from kubetester import create_or_update_configmap, get_pod_when_ready, read_secret, try_load
from kubetester.certs import create_mongodb_tls_certs, create_tls_certs, generate_cert
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.replicaset_search_helper import verify_rs_mongod_parameters
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.tls_utils import (
    assert_tls_handshake,
    cert_serial_number,
    create_crl,
    create_invalid_crl,
    create_keyfile_password_secret,
    encrypt_tls_key_with_password,
)
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-ent-crl"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

# Source MongoDB TLS CA ConfigMap, mounted at tls.CAMountPath (/var/lib/tls/ca/). It carries
# the CRL under crl.pem, alongside the ca-pem/mms-ca.crt keys the operator already expects.
CRL_CA_CONFIGMAP_NAME = "crl-ca"
CRL_CONFIGMAP_KEY = "crl.pem"
CRL_MOUNT_PATH = f"/var/lib/tls/ca/{CRL_CONFIGMAP_KEY}"
MONGOT_CONTAINER = "mongot"
MONGOT_GRPC_PORT = 27028

# MongoDBSearch TLS configuration -- convention: {name}-search-cert
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDB_RESOURCE_NAME)

# Password used to encrypt the gRPC server certificate private key
GRPC_KEY_PASSWORD = "test-grpc-key-password"
# Dedicated secret holding the gRPC keyfile password (spec.security.tls.keyFilePasswordSecretRef)
GRPC_KEY_PASSWORD_SECRET_NAME = f"{MDB_RESOURCE_NAME}-grpc-key-password"

# SCRAM mTLS client cert for mongot -> mongod connection
SCRAM_CLIENT_CERT_SECRET_NAME = f"{MDB_RESOURCE_NAME}-scram-tls-user"
SCRAM_KEY_PASSWORD = "test-scram-key-password"
# Dedicated secret holding the scram keyfile password (spec.source.tls.keyFilePasswordSecretRef)
SCRAM_KEY_PASSWORD_SECRET_NAME = f"{MDB_RESOURCE_NAME}-scram-key-password"

# Client certificates used to exercise the CRL over mongot's gRPC mTLS listener
CLIENT_A_SECRET_NAME = "crl-client-a"
CLIENT_B_SECRET_NAME = "crl-client-b"


@fixture(scope="module")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def crl_ca_configmap(namespace: str) -> str:
    """CA ConfigMap for the source MongoDB TLS that also carries the CRL under crl.pem.

    Seeded with a malformed CRL so mongot fails its first startup; later tests rotate it."""
    ca = open(get_issuer_ca_filepath()).read()
    create_or_update_configmap(
        namespace,
        CRL_CA_CONFIGMAP_NAME,
        {"ca-pem": ca, "mms-ca.crt": ca, CRL_CONFIGMAP_KEY: create_invalid_crl()},
    )
    return CRL_CA_CONFIGMAP_NAME


@fixture(scope="module")
def mdb(namespace: str, crl_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )

    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    resource.configure_custom_tls(crl_ca_configmap, "certs")
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    if "spec" not in resource:
        resource["spec"] = {}
    resource["spec"]["security"] = {
        "tls": {
            "certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME},
            "keyFilePasswordSecretRef": {"name": GRPC_KEY_PASSWORD_SECRET_NAME},
        }
    }
    resource["spec"]["source"] = {"passwordSecretRef": {"name": f"{resource.name}-{MONGOT_USER_NAME}-password"}}
    resource["spec"]["source"]["tls"] = {
        "clientCertificateSecretRef": {"name": SCRAM_CLIENT_CERT_SECRET_NAME},
        "keyFilePasswordSecretRef": {"name": SCRAM_KEY_PASSWORD_SECRET_NAME},
    }
    resource["spec"]["source"]["crlFile"] = CRL_CONFIGMAP_KEY
    try_load(resource)
    return resource


@fixture(scope="module")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="module")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="module")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs.name, MONGOT_USER_NAME)


@fixture(scope="module")
def client_certs(namespace: str, issuer: str) -> dict[str, dict]:
    """Issues two CA-signed client certificates and materialises their PEMs for TLS handshakes.

    Returns a mapping keyed by secret name with the local cert/key paths and the serial number."""
    certs: dict[str, dict] = {}
    for secret_name, dns in ((CLIENT_A_SECRET_NAME, "crl-client-a"), (CLIENT_B_SECRET_NAME, "crl-client-b")):
        generate_cert(
            namespace=namespace,
            pod="",
            dns="",
            issuer=issuer,
            spec={
                "usages": ["digital signature", "key encipherment", "client auth"],
                "dnsNames": [dns],
            },
            secret_name=secret_name,
        )
        secret = read_secret(namespace, secret_name)

        cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w", suffix=".crt")
        cert_file.write(secret["tls.crt"])
        cert_file.close()
        key_file = tempfile.NamedTemporaryFile(delete=False, mode="w", suffix=".key")
        key_file.write(secret["tls.key"])
        key_file.close()

        certs[secret_name] = {
            "cert_path": cert_file.name,
            "key_path": key_file.name,
            "serial": cert_serial_number(secret["tls.crt"]),
        }
    return certs


@mark.e2e_search_enterprise_crl
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_enterprise_crl
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_crl
def test_install_tls_secrets_and_configmaps(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    search_service_name = search_resource_names.mongot_service_name(mdbs.name)
    proxy_service_name = search_resource_names.proxy_service_name(mdbs.name)
    create_tls_certs(
        issuer,
        namespace,
        search_resource_names.mongot_statefulset_name(mdbs.name),
        replicas=1,
        service_name=search_service_name,
        additional_domains=[
            f"{search_service_name}.{namespace}.svc.cluster.local",
            f"{proxy_service_name}.{namespace}.svc.cluster.local",
        ],
        secret_name=MDBS_TLS_SECRET_NAME,
    )

    # Encrypt the gRPC server certificate key with a password and store the password
    # in a dedicated secret referenced via keyFilePasswordSecretRef.
    encrypt_tls_key_with_password(namespace, MDBS_TLS_SECRET_NAME, GRPC_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, GRPC_KEY_PASSWORD_SECRET_NAME, GRPC_KEY_PASSWORD)

    # SCRAM client cert for mongot mTLS to mongod
    generate_cert(
        namespace=namespace,
        pod="",
        dns="",
        issuer=issuer,
        spec={
            "usages": ["digital signature", "key encipherment", "client auth"],
            "dnsNames": ["scram-client"],
        },
        secret_name=SCRAM_CLIENT_CERT_SECRET_NAME,
    )
    encrypt_tls_key_with_password(namespace, SCRAM_CLIENT_CERT_SECRET_NAME, SCRAM_KEY_PASSWORD)
    create_keyfile_password_secret(namespace, SCRAM_KEY_PASSWORD_SECRET_NAME, SCRAM_KEY_PASSWORD)


@mark.e2e_search_enterprise_crl
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_crl
def test_create_users(
    helper: SearchDeploymentHelper, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser, mdb: MongoDB
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_enterprise_crl
def test_malformed_crl_fails_mongot_startup(namespace: str, mdbs: MongoDBSearch, crl_ca_configmap: str):
    # crl_ca_configmap carries a malformed CRL, so mongot must fail startup and the CR must
    # stay out of Running for the bounded wait below.
    mdbs.update()

    reached_running = mdbs.wait_for(
        lambda s: s.get_status_phase() == Phase.Running,
        timeout=120,
        should_raise=False,
    )
    assert reached_running is not True, "MongoDBSearch reached Running despite a malformed CRL"

    pod_name = mongot_pod_name(mdbs)
    pod = get_pod_when_ready(namespace, f"statefulset.kubernetes.io/pod-name={pod_name}", default_retry=10)
    assert pod is None, f"mongot pod {pod_name} became Ready despite a malformed CRL"
    logger.info(f"mongot pod {pod_name} stayed unready with a malformed CRL, as expected")


@mark.e2e_search_enterprise_crl
def test_valid_crl_recovers_mongot(namespace: str, mdbs: MongoDBSearch, crl_ca_configmap: str):
    # Rotate the mounted CRL to a valid one revoking nothing; mongot picks it up and becomes ready.
    rotate_crl(namespace, crl_ca_configmap, create_crl(ca_cert_path(), ca_key_path(), []))
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    # The operator writes crlFile: /var/lib/tls/ca/crl.pem into mongot's TLS blocks and the CRL is
    # mounted from the source CA ConfigMap.
    mongot_cm = KubernetesTester.read_configmap(namespace, search_resource_names.mongot_configmap_name(mdbs.name))
    assert CRL_MOUNT_PATH in mongot_cm["config.yml"], f"crlFile {CRL_MOUNT_PATH} missing from mongot config.yml"

    pod_name = mongot_pod_name(mdbs)
    mounted = KubernetesTester.run_command_in_pod_container(
        pod_name, namespace, ["ls", "/var/lib/tls/ca/"], container=MONGOT_CONTAINER
    )
    assert CRL_CONFIGMAP_KEY in mounted, f"{CRL_CONFIGMAP_KEY} not mounted under /var/lib/tls/ca/: {mounted}"
    logger.info(f"mongot is Running with {CRL_CONFIGMAP_KEY} mounted at {CRL_MOUNT_PATH}")


# After picking up MongoDBSearch CR, MongoDB reconciler will add mongod parameters to each process.
# Due to how MongoDB reconciler works (blocking on waiting for agents and not changing the status to pending)
# the phase won't be updated to Pending and we need to wait by checking agents' status directly in OM.
@mark.e2e_search_enterprise_crl
def test_wait_for_agents_ready(mdb: MongoDB):
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_crl
def test_wait_for_mongod_parameters(mdb: MongoDB):
    expected_host = search_resource_names.mongot_pod_fqdn(mdb.name, mdb.namespace, MONGOT_GRPC_PORT)
    verify_rs_mongod_parameters(mdb.namespace, mdb.name, mdb.get_members(), expected_host)


@fixture(scope="module")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> movies_search_helper.SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(
            mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()
        ),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace, mdb.get_version()),
    )


@mark.e2e_search_enterprise_crl
def test_search_restore_sample_database(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_enterprise_crl
def test_search_create_search_index(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_enterprise_crl
def test_search_assert_search_query(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


@mark.e2e_search_enterprise_crl
def test_revoked_client_cert_is_rejected(
    namespace: str, mdbs: MongoDBSearch, crl_ca_configmap: str, client_certs: dict[str, dict]
):
    crl_pem = create_crl(ca_cert_path(), ca_key_path(), [client_certs[CLIENT_A_SECRET_NAME]["serial"]])
    rotate_crl(namespace, crl_ca_configmap, crl_pem)

    host = mongot_grpc_host(mdbs, namespace)
    wait_for_mounted_crl(namespace, mongot_pod_name(mdbs), crl_pem)
    assert_handshake_eventually(
        host, client_certs[CLIENT_A_SECRET_NAME], should_succeed=False, expected_error="revoked"
    )
    assert_handshake_eventually(host, client_certs[CLIENT_B_SECRET_NAME], should_succeed=True)
    logger.info("revoked client-A rejected; client-B accepted")


@mark.e2e_search_enterprise_crl
def test_crl_reload_without_restart(
    namespace: str, mdbs: MongoDBSearch, crl_ca_configmap: str, client_certs: dict[str, dict]
):
    pod_before = mongot_pod_identity(namespace, mdbs)

    # Rotate the CRL to revoke client-B instead; mongot reloads it without a restart.
    crl_pem = create_crl(ca_cert_path(), ca_key_path(), [client_certs[CLIENT_B_SECRET_NAME]["serial"]])
    rotate_crl(namespace, crl_ca_configmap, crl_pem)

    host = mongot_grpc_host(mdbs, namespace)
    wait_for_mounted_crl(namespace, mongot_pod_name(mdbs), crl_pem)
    assert_handshake_eventually(host, client_certs[CLIENT_A_SECRET_NAME], should_succeed=True)
    assert_handshake_eventually(
        host, client_certs[CLIENT_B_SECRET_NAME], should_succeed=False, expected_error="revoked"
    )

    pod_after = mongot_pod_identity(namespace, mdbs)
    assert pod_after == pod_before, f"mongot pod was replaced: {pod_before} -> {pod_after}"
    logger.info(f"CRL reloaded without restarting mongot pod {pod_before[0]}")


@mark.e2e_search_enterprise_crl
def test_malformed_crl_rotation_keeps_last_good_crl(
    namespace: str, mdbs: MongoDBSearch, crl_ca_configmap: str, client_certs: dict[str, dict]
):
    # Rotating in a malformed CRL must be ignored: the last good CRL (revoking client-B) stays enforced.
    malformed_crl = create_invalid_crl()
    rotate_crl(namespace, crl_ca_configmap, malformed_crl)

    host = mongot_grpc_host(mdbs, namespace)
    wait_for_mounted_crl(namespace, mongot_pod_name(mdbs), malformed_crl)
    assert_handshake_eventually(host, client_certs[CLIENT_A_SECRET_NAME], should_succeed=True)
    assert_handshake_eventually(
        host, client_certs[CLIENT_B_SECRET_NAME], should_succeed=False, expected_error="revoked"
    )
    logger.info("malformed CRL rotation ignored; last good CRL still enforced")


def ca_cert_path() -> str:
    return yaml_fixture("ca-tls.crt")


def ca_key_path() -> str:
    return yaml_fixture("ca-tls.key")


def mongot_pod_name(mdbs: MongoDBSearch) -> str:
    return f"{search_resource_names.mongot_statefulset_name(mdbs.name)}-0"


def mongot_grpc_host(mdbs: MongoDBSearch, namespace: str) -> str:
    return f"{search_resource_names.mongot_service_name(mdbs.name)}.{namespace}.svc.cluster.local"


def mongot_pod_identity(namespace: str, mdbs: MongoDBSearch) -> tuple[str, str]:
    """The (name, uid) of the mongot pod; the uid changes if the pod is replaced."""
    pod = k8s_client.CoreV1Api().read_namespaced_pod(mongot_pod_name(mdbs), namespace)
    return pod.metadata.name, pod.metadata.uid


def rotate_crl(namespace: str, configmap_name: str, crl_pem: str) -> None:
    """Replaces the CRL key in the source CA ConfigMap, preserving its other keys."""
    data = KubernetesTester.read_configmap(namespace, configmap_name)
    data[CRL_CONFIGMAP_KEY] = crl_pem
    create_or_update_configmap(namespace, configmap_name, data)


def wait_for_mounted_crl(namespace: str, pod_name: str, expected_pem: str, timeout: int = 120) -> None:
    """Waits until the CRL mounted in the mongot pod equals expected_pem (ConfigMap propagation is async)."""

    def check() -> tuple:
        content = KubernetesTester.run_command_in_pod_container(
            pod_name, namespace, ["cat", CRL_MOUNT_PATH], container=MONGOT_CONTAINER
        )
        return content.strip() == expected_pem.strip(), "mounted CRL content"

    run_periodically(check, timeout=timeout, sleep_time=5, msg=f"CRL mounted in {pod_name} to update")


def assert_handshake_eventually(
    host: str, cert: dict, should_succeed: bool, expected_error: str | None = None, timeout: int = 120
) -> None:
    """Retries the TLS handshake until it matches should_succeed (mongot reloads the CRL async)."""

    def check() -> tuple:
        try:
            assert_tls_handshake(
                host,
                MONGOT_GRPC_PORT,
                cert["cert_path"],
                cert["key_path"],
                get_issuer_ca_filepath(),
                should_succeed,
                expected_error,
            )
            return True, f"{host}:{MONGOT_GRPC_PORT} handshake matched should_succeed={should_succeed}"
        except AssertionError as e:
            return False, str(e)

    run_periodically(
        check,
        timeout=timeout,
        sleep_time=5,
        msg=f"{host}:{MONGOT_GRPC_PORT} handshake should_succeed={should_succeed}",
    )
