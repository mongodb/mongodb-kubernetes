import tempfile
from typing import List

from kubetester import create_secret, find_fixture
from kubetester.certs import create_mongodb_tls_certs, create_x509_user_cert
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.phase import Phase
from pytest import fixture, mark

USER_NAME = "mms-user-1"
PASSWORD = "my-password"
MDB_RESOURCE = "ldap-replica-set"


@fixture(scope="module")
def server_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, "ldap-replica-set", "certs-ldap-replica-set-cert")
    return "certs"


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    ldap_mongodb_agent_user: LDAPUser,
    server_certs: str,
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})
    ac_secret_name = "automation-config-password"
    create_secret(
        namespace,
        ac_secret_name,
        {"automationConfigPassword": ldap_mongodb_agent_user.password},
    )

    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": server_certs,
        "authentication": {
            "enabled": True,
            "modes": ["LDAP", "SCRAM", "X509"],
            "ldap": {
                "servers": [openldap.servers],
                "bindQueryUser": "cn=admin,dc=example,dc=org",
                "bindQueryPasswordSecretRef": {"name": secret_name},
            },
            "agents": {
                "mode": "LDAP",
                "automationPasswordSecretRef": {
                    "name": ac_secret_name,
                    "key": "automationConfigPassword",
                },
                "automationUserName": ldap_mongodb_agent_user.uid,
            },
        },
    }

    return resource.create()


@fixture(scope="module")
def user_ldap(replica_set: MongoDB, namespace: str, ldap_mongodb_users: List[LDAPUser]) -> MongoDBUser:
    mongodb_user = ldap_mongodb_users[0]
    user = generic_user(
        namespace,
        username=mongodb_user.username,
        db="$external",
        password=mongodb_user.password,
        mongodb_resource=replica_set,
    )
    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    return user.create()


@fixture(scope="module")
def user_scram(replica_set: MongoDB, namespace: str) -> MongoDBUser:
    user = generic_user(
        namespace,
        username="mms-user-1",
        db="admin",
        mongodb_resource=replica_set,
    )
    secret_name = "user-password"
    secret_key = "password"
    create_secret(namespace, secret_name, {secret_key: "my-password"})
    user["spec"]["passwordSecretKeyRef"] = {
        "name": secret_name,
        "key": secret_key,
    }

    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    return user.create()


@mark.e2e_replica_set_ldap
def test_replica_set(replica_set: MongoDB, ldap_mongodb_users: List[LDAPUser]):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap
def test_create_ldap_user(replica_set: MongoDB, user_ldap: MongoDBUser):
    user_ldap.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
    ac.assert_expected_users(1)


@mark.e2e_replica_set_ldap
def test_new_mdb_users_are_created_and_can_authenticate(replica_set: MongoDB, user_ldap: MongoDBUser, ca_path: str):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        attempts=10,
    )


@mark.e2e_replica_set_ldap
def test_create_scram_user(replica_set: MongoDB, user_scram: MongoDBUser):
    user_scram.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
    ac.assert_expected_users(2)


@mark.e2e_replica_set_ldap
def test_replica_set_connectivity(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(ca_path=ca_path)
    tester.assert_connectivity()


@mark.e2e_replica_set_ldap
def test_ops_manager_state_correctly_updated(replica_set: MongoDB, user_ldap: MongoDBUser):
    expected_roles = {
        ("admin", "clusterAdmin"),
        ("admin", "readWriteAnyDatabase"),
        ("admin", "dbAdminAnyDatabase"),
    }

    tester = replica_set.get_automation_config_tester()
    tester.assert_expected_users(2)
    tester.assert_has_user(user_ldap["spec"]["username"])
    tester.assert_user_has_roles(user_ldap["spec"]["username"], expected_roles)

    tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
    tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=3)


@mark.e2e_replica_set_ldap
def test_user_cannot_authenticate_with_incorrect_password(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(ca_path=ca_path)

    tester.assert_scram_sha_authentication_fails(
        password="invalid-password",
        username="mms-user-1",
        auth_mechanism="SCRAM-SHA-256",
        ssl=True,
        tlsCAFile=ca_path,
    )


@mark.e2e_replica_set_ldap
def test_user_can_authenticate_with_correct_password(replica_set: MongoDB, ca_path: str):
    tester = replica_set.tester(ca_path=ca_path)
    tester.assert_scram_sha_authentication(
        password=PASSWORD,
        username="mms-user-1",
        auth_mechanism="SCRAM-SHA-256",
        ssl=True,
        tlsCAFile=ca_path,
    )


@fixture(scope="module")
def user_x509(replica_set: MongoDB, namespace: str) -> MongoDBUser:
    user = generic_user(
        namespace,
        username="CN=x509-testing-user",
        db="$external",
        mongodb_resource=replica_set,
    )

    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    return user.create()


@mark.e2e_replica_set_ldap
def test_x509_user_created(replica_set: MongoDB, user_x509: MongoDBUser):
    user_x509.assert_reaches_phase(Phase.Updated)

    expected_roles = {
        ("admin", "clusterAdmin"),
        ("admin", "readWriteAnyDatabase"),
        ("admin", "dbAdminAnyDatabase"),
    }

    tester = replica_set.get_automation_config_tester()
    tester.assert_expected_users(3)
    tester.assert_has_user(user_x509["spec"]["username"])
    tester.assert_user_has_roles(user_x509["spec"]["username"], expected_roles)

    tester.assert_authentication_mechanism_enabled("MONGODB-X509", active_auth_mechanism=False)


@mark.e2e_replica_set_ldap
def test_x509_user_connectivity(
    namespace: str,
    ca_path: str,
    issuer: str,
    replica_set: MongoDB,
    user_x509: MongoDBUser,
):
    cert_file = tempfile.NamedTemporaryFile(delete=False, mode="w")
    create_x509_user_cert(issuer, namespace, path=cert_file.name)

    tester = replica_set.tester()
    tester.assert_x509_authentication(cert_file_name=cert_file.name, tlsCAFile=ca_path)


@mark.e2e_replica_set_ldap
def test_change_ldap_servers(
    namespace: str,
    replica_set: MongoDB,
    secondary_openldap: OpenLDAP,
    secondary_ldap_mongodb_users: List[LDAPUser],
    secondary_ldap_mongodb_agent_user,
):
    secret_name = "bind-query-password-secondary"
    create_secret(namespace, secret_name, {"password": secondary_openldap.admin_password})
    ac_secret_name = "automation-config-password-secondary"
    create_secret(
        namespace,
        ac_secret_name,
        {"automationConfigPassword": secondary_ldap_mongodb_agent_user.password},
    )
    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["ldap"]["servers"] = [secondary_openldap.servers]
    replica_set["spec"]["security"]["authentication"]["ldap"]["bindQueryPasswordSecretRef"] = {"name": secret_name}
    replica_set["spec"]["security"]["authentication"]["agents"] = {
        "mode": "LDAP",
        "automationPasswordSecretRef": {
            "name": ac_secret_name,
            "key": "automationConfigPassword",
        },
        "automationUserName": secondary_ldap_mongodb_agent_user.uid,
    }

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_ldap
def test_replica_set_ldap_settings_are_updated(replica_set: MongoDB, ldap_mongodb_users: List[LDAPUser]):
    replica_set.reload()
    replica_set["spec"]["security"]["authentication"]["ldap"]["timeoutMS"] = 12345
    replica_set["spec"]["security"]["authentication"]["ldap"]["userCacheInvalidationInterval"] = 60
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    ac = replica_set.get_automation_config_tester()
    assert "timeoutMS" in ac.automation_config["ldap"]
    assert "userCacheInvalidationInterval" in ac.automation_config["ldap"]
    assert ac.automation_config["ldap"]["timeoutMS"] == 12345
    assert ac.automation_config["ldap"]["userCacheInvalidationInterval"] == 60
