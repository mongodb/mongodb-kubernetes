from typing import Dict, List

import pytest
from kubetester import find_fixture, try_load, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.phase import Phase

from .helper_switch_project import SwitchProjectHelper

MDB_RESOURCE_NAME = "sharded-cluster-ldap-switch-project"


@pytest.fixture(scope="module")
def operator_installation_config(operator_installation_config_quick_recovery: Dict[str, str]) -> Dict[str, str]:
    return operator_installation_config_quick_recovery


@pytest.fixture(scope="function")
def sharded_cluster(namespace: str, openldap_tls: OpenLDAP, issuer_ca_configmap: str) -> MongoDB:

    bind_query_password_secret = "bind-query-password"
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-sharded-cluster.yaml"), name=MDB_RESOURCE_NAME, namespace=namespace
    )

    if try_load(resource):
        return resource

    KubernetesTester.create_secret(namespace, bind_query_password_secret, {"password": openldap_tls.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": bind_query_password_secret},
        "transportSecurity": "none",  # For testing CLOUDP-229222
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }
    resource["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM"]

    return resource.update()


@pytest.fixture(scope="function")
def ldap_user_mongodb(sharded_cluster: MongoDB, namespace: str, ldap_mongodb_user_tls: LDAPUser) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user_tls.username,
        db="$external",
        mongodb_resource=sharded_cluster,
        password=ldap_mongodb_user_tls.password,
    )
    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    return user.create()


@pytest.fixture(scope="function")
def testhelper(sharded_cluster: MongoDB, namespace: str) -> SwitchProjectHelper:
    return SwitchProjectHelper(
        resource=sharded_cluster,
        namespace=namespace,
        authentication_mechanism=LDAP_AUTHENTICATION_MECHANISM,
        expected_num_deployment_auth_mechanisms=2,
        active_auth_mechanism=False,
    )


@pytest.mark.e2e_sharded_cluster_ldap_switch_project
class TestShardedClusterLDAPProjectSwitch(KubernetesTester):

    def test_create_sharded_cluster(self, testhelper: SwitchProjectHelper):
        testhelper.test_create_resource()

    def test_sharded_cluster_turn_tls_on_CLOUDP_229222(self, sharded_cluster: MongoDB):

        def wait_for_ac_exists() -> bool:
            ac = sharded_cluster.get_automation_config_tester().automation_config
            try:
                _ = ac["ldap"]["transportSecurity"]
                _ = ac["version"]
                return True
            except KeyError:
                return False

        wait_until(wait_for_ac_exists, timeout=800)
        current_version = sharded_cluster.get_automation_config_tester().automation_config["version"]

        def wait_for_ac_pushed() -> bool:
            ac = sharded_cluster.get_automation_config_tester().automation_config
            try:
                transport_security = ac["ldap"]["transportSecurity"]
                new_version = ac["version"]
                if transport_security != "none":
                    return False
                if new_version <= current_version:
                    return False
                return True
            except KeyError:
                return False

        wait_until(wait_for_ac_pushed, timeout=800)

        sharded_cluster["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
        sharded_cluster.update()

    def test_sharded_cluster_CLOUDP_229222(self, sharded_cluster: MongoDB, ldap_mongodb_users: List[LDAPUser]):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
    # def test_new_mdb_users_are_created(self, ldap_user_mongodb: MongoDBUser):
    #     ldap_user_mongodb.assert_reaches_phase(Phase.Updated)

    def test_ops_manager_state_correctly_updated_in_initial_cluster(self, testhelper: SwitchProjectHelper):
        testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)

    def test_switch_project(self, testhelper: SwitchProjectHelper):
        testhelper.test_switch_project()

    # TODO CLOUDP-349093 - Disabled these tests because project migrations are not supported yet, which could lead to flaky behavior.
    # def test_ops_manager_state_correctly_updated_after_switch(self, testhelper: SwitchProjectHelper):
    #     testhelper.test_ops_manager_state_with_expected_authentication(expected_users=0)
    #     # There should be one user (the previously created user should still exist in the automation configuration). We need to investigate further to understand why the user is not being picked up.
