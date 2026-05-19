from cryptography.hazmat.primitives.serialization import (
    BestAvailableEncryption,
    Encoding,
    PrivateFormat,
    load_pem_private_key,
)
from kubetester import create_or_update_secret, read_secret, try_load, update_secret
from kubetester.certs import create_agent_tls_certs, create_tls_certs, create_x509_mongodb_tls_certs, generate_cert
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator, get_issuer_ca_filepath
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-ent-x509"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

# MongoDBSearch TLS (server cert for gRPC ingress)
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDB_RESOURCE_NAME)

# x509 client cert secret for sync source authentication
X509_CLIENT_CERT_SECRET_NAME = f"{MDB_RESOURCE_NAME}-x509-sync-client-cert"

# The CN used in the x509 client cert -- must match the MongoDB $external user
X509_CLIENT_CERT_CN = "mongot-sync-source"

# Password used to encrypt the x509 client cert private key
X509_AUTH_KEY_PASSWORD = "test-x509-key-password"


def encrypt_x509_key_with_password(namespace: str, secret_name: str, password: str):
    """Encrypts the private key in a TLS secret with a password.

    Reads the cert-manager-generated secret, encrypts tls.key with the given
    password, and updates the secret with the encrypted key and a
    tls.keyFilePassword entry containing the password."""
    secret_data = read_secret(namespace, secret_name)

    private_key = load_pem_private_key(secret_data["tls.key"].encode(), password=None)
    encrypted_key_pem = private_key.private_bytes(
        encoding=Encoding.PEM,
        format=PrivateFormat.TraditionalOpenSSL,
        encryption_algorithm=BestAvailableEncryption(password.encode()),
    )

    update_secret(
        namespace,
        secret_name,
        data={
            "tls.key": encrypted_key_pem.decode(),
            "tls.keyFilePassword": password,
        },
    )
    logger.info(f"Encrypted private key in secret {secret_name} with password")


def get_x509_subject_dn(namespace: str) -> str:
    """Returns the full DN string as MongoDB will interpret it for the $external user.
    Uses O=cluster.local-client to distinguish from internal cluster members
    (which use O=cluster.local-server). MongoDB rejects x509 users whose DN
    matches the internal cluster member pattern."""
    return f"CN={X509_CLIENT_CERT_CN},OU={namespace},O=cluster.local-client,L=NY,ST=NY,C=US"


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDB_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, issuer_ca_configmap: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    resource.configure_custom_tls(issuer_ca_configmap, "certs")
    resource["spec"]["security"]["authentication"] = {
        "enabled": True,
        "modes": ["X509", "SCRAM"],
        "agents": {"mode": "X509"},
        "internalCluster": "X509",
    }
    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    if "spec" not in resource:
        resource["spec"] = {}

    # Server TLS for mongot gRPC (ingress)
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    # x509 client cert for sync source auth (replaces passwordSecretRef)
    resource["spec"]["source"] = {
        "x509": {
            "clientCertificateSecretRef": {"name": X509_CLIENT_CERT_SECRET_NAME},
        },
    }

    try_load(resource)
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(f"{MDB_RESOURCE_NAME}-{ADMIN_USER_NAME}")


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(f"{MDB_RESOURCE_NAME}-{USER_NAME}")


@fixture(scope="function")
def x509_mongot_user(namespace: str, helper: SearchDeploymentHelper) -> MongoDBUser:
    """Creates a MongoDBUser for x509 auth in the $external database."""
    user_dn = get_x509_subject_dn(namespace)
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbuser-search-sync-source-user.yaml"),
        namespace=namespace,
        name=f"{MDB_RESOURCE_NAME}-mongot-x509-user",
    )
    if try_load(resource):
        return resource
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
    resource["spec"]["username"] = user_dn
    resource["spec"]["db"] = "$external"
    # x509 users don't use password auth -- remove passwordSecretKeyRef if present
    resource["spec"].pop("passwordSecretKeyRef", None)
    return resource


@mark.e2e_search_mongot_replicaset_x509_auth
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_mongot_replicaset_x509_auth
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_mongot_replicaset_x509_auth
def test_install_tls_secrets_and_configmaps(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    # Agent certs (required for x509 agent authentication)
    create_agent_tls_certs(issuer, namespace, mdb.name, "certs")
    # MongoDB server certs with x509 subject (clusterfile + per-member)
    create_x509_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-clusterfile")
    create_x509_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    # MongoDBSearch server cert (for gRPC ingress -- includes proxy service SAN)
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

    # x509 client cert for mongot sync source authentication
    x509_subject = {
        "countries": ["US"],
        "provinces": ["NY"],
        "localities": ["NY"],
        "organizations": ["cluster.local-client"],
        "organizationalUnits": [namespace],
    }
    x509_spec = {
        "subject": x509_subject,
        "commonName": X509_CLIENT_CERT_CN,
        "usages": ["digital signature", "key encipherment", "client auth"],
        # Override computed dnsNames -- client certs don't need DNS SANs.
        # generate_cert computes invalid dnsNames when pod/dns are empty,
        # so we override it here. cert-manager accepts this since commonName is set.
        "dnsNames": [X509_CLIENT_CERT_CN],
    }
    generate_cert(
        namespace=namespace,
        pod="",
        dns="",
        issuer=issuer,
        spec=x509_spec,
        secret_name=X509_CLIENT_CERT_SECRET_NAME,
    )

    # Encrypt the x509 client cert private key with a password to test
    # that mongot can handle password-protected keys via tls.keyFilePassword
    encrypt_x509_key_with_password(namespace, X509_CLIENT_CERT_SECRET_NAME, X509_AUTH_KEY_PASSWORD)


@mark.e2e_search_mongot_replicaset_x509_auth
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_mongot_replicaset_x509_auth
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    x509_mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    # Admin user (password auth)
    create_or_update_secret(
        helper.namespace,
        name=admin_user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": ADMIN_USER_PASSWORD},
    )
    admin_user.update()
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    # Regular user (password auth)
    create_or_update_secret(
        helper.namespace,
        name=user["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": USER_PASSWORD},
    )
    user.update()
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    # x509 mongot user (no password needed)
    x509_mongot_user.update()
    x509_mongot_user.assert_reaches_phase(Phase.Updated, timeout=300)


@mark.e2e_search_mongot_replicaset_x509_auth
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_mongot_replicaset_x509_auth
def test_wait_for_agents_ready(mdb: MongoDB):
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@fixture(scope="function")
def sample_movies_helper(mdb: MongoDB, namespace: str) -> movies_search_helper.SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester.for_replicaset(
            mdb, f"{MDB_RESOURCE_NAME}-{USER_NAME}", USER_PASSWORD, use_ssl=True, ca_path=get_issuer_ca_filepath()
        ),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_search_mongot_replicaset_x509_auth
def test_search_restore_sample_database(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_mongot_replicaset_x509_auth
def test_search_create_search_index(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_mongot_replicaset_x509_auth
def test_search_assert_search_query(sample_movies_helper: movies_search_helper.SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)
