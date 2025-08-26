import time
from typing import Dict, List

from kubetester import wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.mongotester import ShardedClusterTester
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def operator_installation_config(operator_installation_config_quick_recovery: Dict[str, str]) -> Dict[str, str]:
    return operator_installation_config_quick_recovery


@fixture(scope="module")
def sharded_cluster(
    openldap_tls: OpenLDAP,
    namespace: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    bind_query_password_secret = "bind-query-password"
    resource = MongoDB.from_yaml(yaml_fixture("ldap/ldap-sharded-cluster.yaml"), namespace=namespace)

    KubernetesTester.create_secret(namespace, bind_query_password_secret, {"password": openldap_tls.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": bind_query_password_secret},
        "transportSecurity": "none",  # For testing CLOUDP-229222
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }
    resource["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM"]

    return resource.create()


@fixture(scope="module")
def ldap_user_mongodb(replica_set: MongoDB, namespace: str, ldap_mongodb_user_tls: LDAPUser) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user_tls.username,
        db="$external",
        mongodb_resource=replica_set,
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


@mark.e2e_sharded_cluster_ldap
def test_sharded_cluster_pending_CLOUDP_229222(sharded_cluster: MongoDB):
    """
    This function tests CLOUDP-229222. The resource needs to enter the "Pending" state and without the automatic
    recovery, it would stay like this forever (since we wouldn't push the new AC with a fix).
    """
    sharded_cluster.assert_reaches_phase(Phase.Pending, timeout=100)


@mark.e2e_sharded_cluster_ldap
def test_sharded_cluster_turn_tls_on_CLOUDP_229222(sharded_cluster: MongoDB):
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


@mark.e2e_sharded_cluster_ldap
def test_sharded_cluster_CLOUDP_229222(sharded_cluster: MongoDB):
    """
    This function tests CLOUDP-229222. The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
    goes into running state.
    """
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1400)


# TODO: Move to a MongoDBUsers (based on KubeObject) for this user creation.
@mark.e2e_sharded_cluster_ldap
class TestAddLDAPUsers(KubernetesTester):
    """
    name: Create LDAP Users
    create_many:
      file: ldap/ldap-sharded-cluster-user.yaml
      wait_until: all_users_ready
    """

    def test_users_ready(self):
        pass

    @staticmethod
    def all_users_ready():
        ac = KubernetesTester.get_automation_config()
        automation_config_users = 0
        for user in ac["auth"]["usersWanted"]:
            if user["user"] != "mms-backup-agent" and user["user"] != "mms-monitoring-agent":
                automation_config_users += 1

        return automation_config_users == 1


@mark.e2e_sharded_cluster_ldap
def test_new_mdb_users_are_created(sharded_cluster: MongoDB, ldap_mongodb_users: List[LDAPUser]):
    tester = ShardedClusterTester(sharded_cluster.name, 1)

    for ldap_user in ldap_mongodb_users:
        tester.assert_ldap_authentication(ldap_user.username, ldap_user.password, attempts=10)
