import datetime
from pathlib import Path

import pymongo
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID
from kubetester import create_or_update_configmap, create_or_update_secret
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from pytest import TempPathFactory, fixture, mark
from tests import test_logger
from tests.common.search import movies_search_helper
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.conftest import get_default_operator

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "mongot-user"
MONGOT_USER_PASSWORD = "mongot-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MDBC_RESOURCE_NAME = "mdbc-rs"

TLS_SECRET_NAME = "tls-secret"
TLS_CA_CONFIGMAP_NAME = "tls-ca-configmap"

# MongoDBSearch TLS configuration
MDBS_TLS_SECRET_NAME = "mdbs-tls-secret"


@fixture(scope="module")
def ca_certificate(tmp_path_factory: TempPathFactory):
    """Generate a CA certificate and private key"""
    # Generate CA private key
    ca_private_key = rsa.generate_private_key(
        public_exponent=65537,
        key_size=2048,
    )

    # Generate CA certificate
    ca_subject = x509.Name(
        [
            x509.NameAttribute(NameOID.COUNTRY_NAME, "US"),
            x509.NameAttribute(NameOID.STATE_OR_PROVINCE_NAME, "CA"),
            x509.NameAttribute(NameOID.LOCALITY_NAME, "San Francisco"),
            x509.NameAttribute(NameOID.ORGANIZATION_NAME, "MongoDB Test CA"),
            x509.NameAttribute(NameOID.COMMON_NAME, "MongoDB Test Root CA"),
        ]
    )

    ca_cert = (
        x509.CertificateBuilder()
        .subject_name(ca_subject)
        .issuer_name(ca_subject)
        .public_key(ca_private_key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(datetime.datetime.utcnow())
        .not_valid_after(datetime.datetime.utcnow() + datetime.timedelta(days=365))
        .add_extension(
            x509.BasicConstraints(ca=True, path_length=None),
            critical=True,
        )
        .add_extension(
            x509.KeyUsage(
                digital_signature=True,
                key_cert_sign=True,
                crl_sign=True,
                key_agreement=False,
                key_encipherment=False,
                data_encipherment=False,
                content_commitment=False,
                encipher_only=False,
                decipher_only=False,
            ),
            critical=True,
        )
        .sign(ca_private_key, hashes.SHA256())
    )

    cert_file_path = tmp_path_factory.mktemp("certs").joinpath("ca.crt")
    cert_file_path.write_bytes(ca_cert.public_bytes(serialization.Encoding.PEM))

    return ca_cert, ca_private_key, cert_file_path


def generate_server_certificate(
    ca_cert, ca_private_key, namespace: str, resource_name: str, resource_type: str = "mdbc"
):
    # Generate server private key
    server_private_key = rsa.generate_private_key(
        public_exponent=65537,
        key_size=2048,
    )

    # Generate server certificate
    server_subject = x509.Name(
        [
            x509.NameAttribute(NameOID.COUNTRY_NAME, "US"),
            x509.NameAttribute(NameOID.STATE_OR_PROVINCE_NAME, "CA"),
            x509.NameAttribute(NameOID.LOCALITY_NAME, "San Francisco"),
            x509.NameAttribute(NameOID.ORGANIZATION_NAME, "MongoDB Test"),
            x509.NameAttribute(NameOID.COMMON_NAME, f"{resource_name}.{namespace}.svc.cluster.local"),
        ]
    )

    # Create SAN list based on resource type
    if resource_type == "mdbc":
        # MongoDBCommunity StatefulSet pods and services
        san_list = [
            x509.DNSName(f"{resource_name}-0.{resource_name}-svc.{namespace}.svc.cluster.local"),
            x509.DNSName(f"{resource_name}-1.{resource_name}-svc.{namespace}.svc.cluster.local"),
            x509.DNSName(f"{resource_name}-2.{resource_name}-svc.{namespace}.svc.cluster.local"),
            x509.DNSName(f"{resource_name}-svc.{namespace}.svc.cluster.local"),
        ]
    elif resource_type == "mdbs":
        # MongoDBSearch service names
        san_list = [
            x509.DNSName(f"{resource_name}-search-svc.{namespace}.svc.cluster.local"),
        ]

    server_cert = (
        x509.CertificateBuilder()
        .subject_name(server_subject)
        .issuer_name(ca_cert.subject)
        .public_key(server_private_key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(datetime.datetime.utcnow())
        .not_valid_after(datetime.datetime.utcnow() + datetime.timedelta(days=365))
        .add_extension(
            x509.SubjectAlternativeName(san_list),
            critical=False,
        )
        .add_extension(
            x509.BasicConstraints(ca=False, path_length=None),
            critical=True,
        )
        .add_extension(
            x509.KeyUsage(
                digital_signature=True,
                key_encipherment=True,
                key_agreement=False,
                key_cert_sign=False,
                crl_sign=False,
                data_encipherment=False,
                content_commitment=False,
                encipher_only=False,
                decipher_only=False,
            ),
            critical=True,
        )
        .add_extension(
            x509.ExtendedKeyUsage(
                [
                    x509.oid.ExtendedKeyUsageOID.SERVER_AUTH,
                    x509.oid.ExtendedKeyUsageOID.CLIENT_AUTH,
                ]
            ),
            critical=True,
        )
        .sign(ca_private_key, hashes.SHA256())
    )

    return {
        "cert": server_cert.public_bytes(serialization.Encoding.PEM).decode("utf-8"),
        "key": server_private_key.private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.PKCS8,
            encryption_algorithm=serialization.NoEncryption(),
        ).decode("utf-8"),
    }


@fixture(scope="module")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-sample-mflix.yaml"),
        name=MDBC_RESOURCE_NAME,
        namespace=namespace,
    )

    # Add TLS configuration
    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "certificateKeySecretRef": {"name": TLS_SECRET_NAME},
        "caConfigMapRef": {"name": TLS_CA_CONFIGMAP_NAME},
    }

    return resource


@fixture(scope="module")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"),
        namespace=namespace,
    )

    # Add TLS configuration to MongoDBSearch
    if "spec" not in resource:
        resource["spec"] = {}

    resource["spec"]["security"] = {"tls": {"enabled": True, "certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}

    return resource


@mark.e2e_search_community_tls
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_community_tls
def test_install_secrets(namespace: str, mdbs: MongoDBSearch):
    # Create user password secrets
    create_or_update_secret(namespace=namespace, name=f"{USER_NAME}-password", data={"password": USER_PASSWORD})
    create_or_update_secret(
        namespace=namespace, name=f"{ADMIN_USER_NAME}-password", data={"password": ADMIN_USER_PASSWORD}
    )
    create_or_update_secret(
        namespace=namespace, name=f"{mdbs.name}-{MONGOT_USER_NAME}-password", data={"password": MONGOT_USER_PASSWORD}
    )


@mark.e2e_search_community_tls
def test_install_tls_secrets_and_configmaps(
    namespace: str, mdbc: MongoDBCommunity, mdbs: MongoDBSearch, ca_certificate
):
    mongodb_certs = generate_server_certificate(ca_certificate[0], ca_certificate[1], namespace, mdbc.name, "mdbc")
    mongodb_tls_cert_data = {"tls.crt": mongodb_certs["cert"], "tls.key": mongodb_certs["key"]}
    create_or_update_secret(
        namespace=namespace, name=TLS_SECRET_NAME, data=mongodb_tls_cert_data, type="kubernetes.io/tls"
    )

    search_certs = generate_server_certificate(ca_certificate[0], ca_certificate[1], namespace, mdbs.name, "mdbs")
    search_tls_cert_data = {"tls.crt": search_certs["cert"], "tls.key": search_certs["key"]}
    create_or_update_secret(
        namespace=namespace, name=MDBS_TLS_SECRET_NAME, data=search_tls_cert_data, type="kubernetes.io/tls"
    )

    ca_data = {"ca.crt": ca_certificate[2].read_text(encoding="utf-8")}  # Read CA cert from file
    create_or_update_configmap(namespace=namespace, name=TLS_CA_CONFIGMAP_NAME, data=ca_data)


@mark.e2e_search_community_tls
def test_create_database_resource(mdbc: MongoDBCommunity):
    mdbc.update()
    mdbc.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_search_community_tls
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_community_tls
def test_wait_for_community_resource_ready(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_search_community_tls
def test_validate_tls_connections(mdbc: MongoDBCommunity, mdbs: MongoDBSearch, namespace: str, ca_certificate):
    ca_file_path = str(ca_certificate[2])

    with pymongo.MongoClient(
        f"mongodb://{mdbc.name}-0.{mdbc.name}-svc.{namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}",
        tls=True,
        tlsCAFile=ca_file_path,
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=30000,
        connectTimeoutMS=20000,
    ) as mongodb_client:
        mongodb_info = mongodb_client.admin.command("hello")
        assert mongodb_info.get("ok") == 1, "MongoDBCommunity connection failed"

    with pymongo.MongoClient(
        f"mongodb://{mdbs.name}-search-svc.{namespace}.svc.cluster.local:27027",
        tls=True,
        tlsCAFile=ca_file_path,
        tlsAllowInvalidHostnames=False,
        serverSelectionTimeoutMS=10000,
        connectTimeoutMS=10000,
    ) as search_client:
        search_info = search_client.admin.command("hello")
        assert search_info.get("ok") == 1, "MongoDBSearch connection failed"


@fixture(scope="function")
def sample_movies_helper(mdbc: MongoDBCommunity, ca_certificate) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            get_connection_string(mdbc, USER_NAME, USER_PASSWORD), use_ssl=True, ca_path=str(ca_certificate[2])
        ),
    )


@mark.e2e_search_community_tls
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_search_community_tls
def test_search_create_search_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_search_community_tls
def test_search_assert_search_query(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.assert_search_query(retry_timeout=60)


def get_connection_string(mdbc: MongoDBCommunity, user_name: str, user_password: str) -> str:
    return f"mongodb://{user_name}:{user_password}@{mdbc.name}-0.{mdbc.name}-svc.{mdbc.namespace}.svc.cluster.local:27017/?replicaSet={mdbc.name}"
