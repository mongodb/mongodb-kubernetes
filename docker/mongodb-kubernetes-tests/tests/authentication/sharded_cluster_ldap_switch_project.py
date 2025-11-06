import time
from typing import Dict, List

import pytest
from kubetester import (
    create_or_update_configmap,
    create_secret,
    find_fixture,
    random_k8s_name,
    read_configmap,
    wait_until,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase

CONFIG_MAP_KEYS = {
    "BASE_URL": "baseUrl",
    "PROJECT_NAME": "projectName",
    "ORG_ID": "orgId",
}

MDB_RESOURCE_NAME = "sharded-cluster-ldap-switch-project"
MDB_FIXTURE_NAME = MDB_RESOURCE_NAME


@pytest.fixture(scope="module")
def operator_installation_config(operator_installation_config_quick_recovery: Dict[str, str]) -> Dict[str, str]:
    return operator_installation_config_quick_recovery


@pytest.fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project-")


@pytest.fixture(scope="module")
def sharded_cluster(namespace: str, openldap_tls: OpenLDAP, issuer_ca_configmap: str) -> MongoDB:

    bind_query_password_secret = "bind-query-password"
    resource = MongoDB.from_yaml(find_fixture(f"switch-project/{MDB_FIXTURE_NAME}.yaml"), namespace=namespace)

    KubernetesTester.create_secret(namespace, bind_query_password_secret, {"password": openldap_tls.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": bind_query_password_secret},
        "transportSecurity": "none",  # For testing CLOUDP-229222
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }
    resource["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM"]

    return resource


@pytest.fixture(scope="module")
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


@pytest.mark.e2e_sharded_cluster_ldap_switch_project
class TestShardedClusterLDAPProjectSwitch(KubernetesTester):

    def test_create_sharded_cluster(self, sharded_cluster: MongoDB):
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Pending, timeout=600)

    def test_sharded_cluster_turn_tls_on_CLOUDP_229222(self, sharded_cluster: MongoDB):
        """
        This function tests CLOUDP-229222. The user attempts to fix the AutomationConfig.
        Before updating the AutomationConfig, we need to ensure the operator pushed the wrong one to Ops Manager.
        """

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

        resource = sharded_cluster.load()
        resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
        resource.update()

    def test_sharded_cluster_CLOUDP_229222(self, sharded_cluster: MongoDB, ldap_mongodb_users: List[LDAPUser]):
        """
        This function tests CLOUDP-229222. The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
        goes into running state.
        """
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=800)

    def test_ops_manager_state_correctly_updated_in_initial_cluster(
        self, sharded_cluster: MongoDB, ldap_user_mongodb: MongoDBUser
    ):

        ldap_user_mongodb.assert_reaches_phase(Phase.Updated)
        ac_tester = sharded_cluster.get_automation_config_tester()
        ac_tester.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
        ac_tester.assert_expected_users(1)

    def test_switch_sharded_cluster_project(self, sharded_cluster: MongoDB, namespace: str, project_name_prefix: str):
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

        sharded_cluster.reload()
        sharded_cluster["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        sharded_cluster.update()
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_ops_manager_state_correctly_updated_in_moved_cluster(self, sharded_cluster: MongoDB):
        ac_tester = sharded_cluster.get_automation_config_tester()
        ac_tester.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
        # ac_tester.assert_expected_users(1)
