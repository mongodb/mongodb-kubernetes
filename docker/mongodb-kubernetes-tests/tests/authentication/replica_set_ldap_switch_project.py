import tempfile
from typing import List

import pytest
from kubetester import (
    create_or_update_configmap,
    create_secret,
    find_fixture,
    random_k8s_name,
    read_configmap,
)
from kubetester.certs import create_mongodb_tls_certs, create_x509_user_cert
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.phase import Phase

MDB_RESOURCE_NAME = "replica-set-ldap-switch-project"
MDB_FIXTURE_NAME = MDB_RESOURCE_NAME

CONFIG_MAP_KEYS = {
    "BASE_URL": "baseUrl",
    "PROJECT_NAME": "projectName",
    "ORG_ID": "orgId",
}


@pytest.fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    """
    Generates a random Kubernetes project name prefix based on the namespace.

    Ensures test isolation in a multi-namespace test environment.
    """
    return random_k8s_name(f"{namespace}-project-")


@pytest.fixture(scope="module")
def server_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, MDB_RESOURCE_NAME, "certs-" + MDB_RESOURCE_NAME + "-cert")
    return "certs"


@pytest.fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    ldap_mongodb_agent_user: LDAPUser,
    server_certs: str,
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture(f"switch-project/{MDB_FIXTURE_NAME}.yaml"), namespace=namespace)

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

    return resource


@pytest.fixture(scope="module")
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


@pytest.mark.e2e_replica_set_ldap_switch_project
class TestReplicaSetLDAPProjectSwitch(KubernetesTester):

    def test_create_replica_set(self, replica_set: MongoDB, ldap_mongodb_users: List[LDAPUser]):
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_ldap_user(self, replica_set: MongoDB, user_ldap: MongoDBUser):
        user_ldap.assert_reaches_phase(Phase.Updated)

        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
        tester.assert_expected_users(1)

    def test_new_mdb_users_are_created_and_can_authenticate(
        self, replica_set: MongoDB, user_ldap: MongoDBUser, ca_path: str
    ):
        tester = replica_set.tester()

        tester.assert_ldap_authentication(
            username=user_ldap["spec"]["username"],
            password=user_ldap.password,
            tls_ca_file=ca_path,
            attempts=10,
        )

    def test_switch_replica_set_project(
        self, replica_set: MongoDB, namespace: str, project_name_prefix: str, user_ldap: MongoDBUser
    ):
        """
        Modify the replica set to switch its Ops Manager reference to a new project and verify lifecycle.
        """
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        new_project_name = f"{project_name_prefix}-second"
        new_project_configmap = create_or_update_configmap(
            namespace=namespace,
            name=new_project_name,
            data={
                CONFIG_MAP_KEYS["BASE_URL"]: original_configmap[CONFIG_MAP_KEYS["BASE_URL"]],
                CONFIG_MAP_KEYS["PROJECT_NAME"]: new_project_name,
                CONFIG_MAP_KEYS["ORG_ID"]: original_configmap[CONFIG_MAP_KEYS["ORG_ID"]],
            },
        )

        replica_set.load()
        replica_set["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_moved_cluster(self, replica_set: MongoDB,  user_ldap: MongoDBUser, ca_path: str):
        tester = replica_set.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
        # tester.assert_expected_users(1)

        # tester = replica_set.tester()
        # tester.assert_ldap_authentication(
        #     username=user_ldap["spec"]["username"],
        #     password=user_ldap.password,
        #     tls_ca_file=ca_path,
        #     attempts=10,
        # )
