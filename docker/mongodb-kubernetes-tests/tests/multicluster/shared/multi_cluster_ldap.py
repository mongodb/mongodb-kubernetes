from kubetester import wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_mongodb_multi_pending(mongodb_multi: MongoDBMulti | MongoDB):
    """
    This function tests CLOUDP-229222. The resource needs to enter the "Pending" state and without the automatic
    recovery, it would stay like this forever (since we wouldn't push the new AC with a fix).
    """
    mongodb_multi.assert_reaches_phase(Phase.Pending, timeout=100)


def test_turn_tls_on_CLOUDP_229222(mongodb_multi: MongoDBMulti | MongoDB):
    """
    This function tests CLOUDP-229222. The user attempts to fix the AutomationConfig.
    Before updating the AutomationConfig, we need to ensure the operator pushed the wrong one to Ops Manager.
    """

    def wait_for_ac_exists() -> bool:
        ac = mongodb_multi.get_automation_config_tester().automation_config
        try:
            _ = ac["ldap"]["transportSecurity"]
            _ = ac["version"]
            return True
        except KeyError:
            return False

    wait_until(wait_for_ac_exists, timeout=200)
    current_version = mongodb_multi.get_automation_config_tester().automation_config["version"]

    def wait_for_ac_pushed() -> bool:
        ac = mongodb_multi.get_automation_config_tester().automation_config
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

    wait_until(wait_for_ac_pushed, timeout=500)

    resource = mongodb_multi.load()

    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    resource.update()


def test_multi_replicaset_CLOUDP_229222(mongodb_multi: MongoDBMulti | MongoDB):
    """
    This function tests CLOUDP-229222.  The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
    goes into running state.
    """
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1900)


def test_restore_mongodb_multi_ldap_configuration(mongodb_multi: MongoDBMulti | MongoDB):
    """
    This function restores the initial desired security configuration to carry on with the next tests normally.
    """
    resource = mongodb_multi.load()

    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP"]
    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    resource["spec"]["security"]["authentication"]["agents"]["mode"] = "LDAP"

    resource.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


def test_create_ldap_user(mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser):
    user_ldap.assert_reaches_phase(Phase.Updated)
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
    ac.assert_expected_users(1)


def test_ldap_user_created_and_can_authenticate(
    mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        attempts=10,
    )


def test_ops_manager_state_correctly_updated(mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser):
    expected_roles = {
        ("admin", "clusterAdmin"),
        ("admin", "readWriteAnyDatabase"),
        ("admin", "dbAdminAnyDatabase"),
    }
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_expected_users(1)
    ac.assert_has_user(user_ldap["spec"]["username"])
    ac.assert_user_has_roles(user_ldap["spec"]["username"], expected_roles)
    ac.assert_authentication_mechanism_enabled("PLAIN", active_auth_mechanism=True)
    ac.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=1)

    assert "userCacheInvalidationInterval" in ac.automation_config["ldap"]
    assert "timeoutMS" in ac.automation_config["ldap"]
    assert ac.automation_config["ldap"]["userCacheInvalidationInterval"] == 60
    assert ac.automation_config["ldap"]["timeoutMS"] == 12345


def test_deployment_is_reachable_with_ldap_agent(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable()


def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_names):
    mongodb_multi.reload()
    mongodb_multi["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


def test_new_ldap_user_can_authenticate_after_scaling(
    mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        attempts=10,
    )


def test_disable_agent_auth(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.reload()
    mongodb_multi["spec"]["security"]["authentication"]["enabled"] = False
    mongodb_multi["spec"]["security"]["authentication"]["agents"]["enabled"] = False
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_mongodb_multi_connectivity_with_no_auth(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


def test_deployment_is_reachable_with_no_auth(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable()
