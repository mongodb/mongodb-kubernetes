import tempfile
from typing import List

import pytest
from kubetester import (
    create_secret,
    find_fixture,
    read_configmap,
    try_load,
)
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.phase import Phase

from .replica_set_switch_project_helper import (
    ReplicaSetCreationAndProjectSwitchTestHelper,
)

MDB_RESOURCE_NAME = "replica-set-ldap-switch-project"


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
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-replica-set.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

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

    return resource.update()


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


@pytest.fixture(scope="module")
def test_helper(replica_set: MongoDB, namespace: str) -> ReplicaSetCreationAndProjectSwitchTestHelper:
    return ReplicaSetCreationAndProjectSwitchTestHelper(
        replica_set=replica_set,
        namespace=namespace,
        authentication_mechanism=LDAP_AUTHENTICATION_MECHANISM,
        expected_num_deployment_auth_mechanisms=3,
    )


@pytest.mark.e2e_replica_set_ldap_switch_project
class TestReplicaSetLDAPProjectSwitch(KubernetesTester):

    def test_create_replica_set(self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper):
        test_helper.test_create_replica_set()

    def test_create_ldap_user(self, user_ldap: MongoDBUser):
        user_ldap.assert_reaches_phase(Phase.Updated)

    def test_ops_manager_state_correctly_updated_in_initial_replica_set(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=1)

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
        self, namespace: str, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        original_configmap = read_configmap(namespace=namespace, name="my-project")
        test_helper.test_switch_replica_set_project(
            original_configmap, new_project_configmap_name=namespace + "-" + "second"
        )

    def test_ops_manager_state_correctly_updated_in_moved_cluster(
        self, test_helper: ReplicaSetCreationAndProjectSwitchTestHelper
    ):
        test_helper.test_ops_manager_state_with_expected_authentication(expected_users=0)

        # tester.assert_expected_users(1)

        # tester = replica_set.tester()
        # tester.assert_ldap_authentication(
        #     username=user_ldap["spec"]["username"],
        #     password=user_ldap.password,
        #     tls_ca_file=ca_path,
        #     attempts=10,
        # )
