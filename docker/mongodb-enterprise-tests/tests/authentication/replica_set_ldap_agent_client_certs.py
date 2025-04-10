import tempfile

from kubetester import create_secret, delete_secret, find_fixture, read_secret
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, generate_cert
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from pytest import fixture, mark

USER_NAME = "mms-user-1"
PASSWORD = "my-password"


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    resource_name = "ldap-replica-set"
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        resource_name,
        f"{resource_name}-cert",
        replicas=5,
    )


@fixture(scope="module")
def client_cert_path(issuer: str, namespace: str):
    spec = {
        "commonName": "client-cert",
        "subject": {"organizationalUnits": ["mongodb.com"]},
    }

    client_secret = generate_cert(
        namespace,
        "client-cert",
        "client-cert",
        issuer,
        spec=spec,
    )
    client_cert = read_secret(namespace, client_secret)
    cert_file = tempfile.NamedTemporaryFile()
    with open(cert_file.name, "w") as f:
        f.write(client_cert["tls.key"] + client_cert["tls.crt"])

    yield cert_file.name

    cert_file.close()


@fixture(scope="module")
def agent_client_cert(issuer: str, namespace: str) -> str:
    spec = {
        "commonName": "mms-automation-client-cert",
        "subject": {"organizationalUnits": ["mongodb.com"]},
    }

    client_certificate_secret = generate_cert(
        namespace,
        "mongodb-mms-automation",
        "mongodb-mms-automation",
        issuer,
        spec=spec,
    )
    automation_agent_cert = read_secret(namespace, client_certificate_secret)
    data = {}
    data["tls.crt"], data["tls.key"] = (
        automation_agent_cert["tls.crt"],
        automation_agent_cert["tls.key"],
    )
    # creates a secret that combines key and crt
    create_secret(namespace, "agent-client-cert", data, type="kubernetes.io/tls")

    yield "agent-client-cert"


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer: str,
    issuer_ca_configmap: str,
    server_certs: str,
    agent_client_cert: str,
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-agent-auth.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})

    ac_secret_name = "automation-config-password"
    create_secret(namespace, ac_secret_name, {"automationConfigPassword": "LDAPPassword."})

    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "ca": issuer_ca_configmap,
    }

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
    }
    resource["spec"]["security"]["authentication"]["agents"] = {
        "mode": "LDAP",
        "automationPasswordSecretRef": {
            "name": ac_secret_name,
            "key": "automationConfigPassword",
        },
        "clientCertificateSecretRef": {"name": agent_client_cert},
        "automationUserName": "mms-automation-agent",
    }

    return resource.update()


@fixture(scope="module")
def ldap_user_mongodb(replica_set: MongoDB, namespace: str, ldap_mongodb_user: LDAPUser) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user.uid,
        db="$external",
        mongodb_resource=replica_set,
        password=ldap_mongodb_user.password,
    )
    user.add_roles(
        [
            # In order to be able to write to custom db/collections during the tests
            Role(db="admin", role="readWriteAnyDatabase"),
        ]
    )

    return user.update()


@mark.e2e_replica_set_ldap_agent_client_certs
@mark.usefixtures("ldap_mongodb_agent_user", "ldap_user_mongodb")
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400, ignore_errors=True)


@mark.e2e_replica_set_ldap_agent_client_certs
def test_ldap_user_mongodb_reaches_updated_phase(ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated, timeout=150)


@mark.e2e_replica_set_ldap_agent_client_certs
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser, ca_path: str):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        tls_ca_file=ca_path,
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_requires_certs(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["requireClientTLSAuthentication"] = True
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    ac_tester = replica_set.get_automation_config_tester()
    ac_tester.assert_tls_client_certificate_mode("REQUIRE")


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_can_auth_with_client_certs_provided(
    replica_set: MongoDB,
    ldap_user_mongodb: MongoDBUser,
    ca_path: str,
    client_cert_path: str,
):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        tls_ca_file=ca_path,
        ssl_certfile=client_cert_path,
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_certs_made_optional(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["requireClientTLSAuthentication"] = False
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    ac_tester = replica_set.get_automation_config_tester()
    ac_tester.assert_tls_client_certificate_mode("OPTIONAL")


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_can_auth_again_with_no_client_certs(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser, ca_path: str):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        tls_ca_file=ca_path,
        attempts=10,
    )
