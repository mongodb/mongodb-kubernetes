import tempfile

from pytest import mark, fixture

from kubetester import create_secret, read_secret, delete_secret, find_fixture

from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, generic_user, Role
from kubetester.ldap import OpenLDAP, LDAPUser
from kubetester.certs import generate_cert

USER_NAME = "mms-user-1"
PASSWORD = "my-password"


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    # TODO: move into a `replica_set` fixture that initializes with TLS and
    # no need to worry about the tls things on every test.
    resource_name = "ldap-replica-set"
    pod_fqdn_fstring = "{resource_name}-{index}.{resource_name}-svc.{namespace}.svc.cluster.local".format(
        resource_name=resource_name,
        namespace=namespace,
        index="{}",
    )
    data = {}
    for i in range(3):
        pod_dns = pod_fqdn_fstring.format(i)
        pod_name = f"{resource_name}-{i}"
        cert = generate_cert(namespace, pod_dns, pod_name, issuer)
        secret = read_secret(namespace, cert)
        data[pod_name + "-pem"] = secret["tls.key"] + secret["tls.crt"]

    create_secret(namespace, f"{resource_name}-cert", data)

    yield f"{resource_name}-cert"

    delete_secret(namespace, f"{resource_name}-cert")


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

    # creates a secret that combines key and crt
    create_secret(
        namespace,
        "agent-client-cert",
        {
            "mms-automation-agent-pem": automation_agent_cert["tls.key"]
            + automation_agent_cert["tls.crt"],
        },
    )

    yield "agent-client-cert"

    delete_secret(namespace, "agent-client-cert")


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer: str,
    issuer_ca_configmap: str,
    server_certs: str,
    agent_client_cert: str,
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-agent-auth.yaml"), namespace=namespace
    )

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})

    ac_secret_name = "automation-config-password"
    create_secret(
        namespace, ac_secret_name, {"automationConfigPassword": "LDAPPassword."}
    )

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

    return resource.create()


@fixture(scope="module")
def ldap_user_mongodb(
    replica_set: MongoDB, namespace: str, ldap_mongodb_user: LDAPUser
) -> MongoDBUser:
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

    return user.create()


@mark.e2e_replica_set_ldap_agent_client_certs
@mark.usefixtures("ldap_mongodb_agent_user", "ldap_user_mongodb")
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_agent_client_certs
def test_new_ldap_users_can_authenticate(
    replica_set: MongoDB, ldap_user_mongodb: MongoDBUser, ca_path: str
):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        ssl_ca_certs=ca_path,
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_requires_certs(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"][
        "requireClientTLSAuthentication"
    ] = True
    replica_set.update()

    replica_set.assert_abandons_phase(Phase.Running, timeout=400)
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
        ssl_ca_certs=ca_path,
        ssl_certfile=client_cert_path,
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_certs_made_optional(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["authentication"][
        "requireClientTLSAuthentication"
    ] = False
    replica_set.update()

    replica_set.assert_abandons_phase(Phase.Running, timeout=400)
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    ac_tester = replica_set.get_automation_config_tester()
    ac_tester.assert_tls_client_certificate_mode("OPTIONAL")


@mark.e2e_replica_set_ldap_agent_client_certs
def test_client_can_auth_again_with_no_client_certs(
    replica_set: MongoDB, ldap_user_mongodb: MongoDBUser, ca_path: str
):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        ssl_ca_certs=ca_path,
        attempts=10,
    )
