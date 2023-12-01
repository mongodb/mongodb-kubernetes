from typing import List

from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester
from pytest import fixture, mark


@fixture(scope="module")
def sharded_cluster(openldap: OpenLDAP, namespace: str) -> MongoDB:
    bind_query_password_secret = "bind-query-password"
    resource = MongoDB.from_yaml(yaml_fixture("ldap/ldap-sharded-cluster.yaml"), namespace=namespace)

    KubernetesTester.create_secret(namespace, bind_query_password_secret, {"password": openldap.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryPasswordSecretRef": {
            "name": bind_query_password_secret,
        },
    }
    resource["spec"]["security"]["authentication"]["agents"] = {"mode": "SCRAM"}
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM"]

    return resource.create()


@mark.e2e_sharded_cluster_ldap
def test_sharded_cluster_is_running(sharded_cluster: MongoDB, ldap_mongodb_users: List[LDAPUser]):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)


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
