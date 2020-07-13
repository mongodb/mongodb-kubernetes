import time
from typing import List

from kubernetes import client
from pytest import mark, fixture

from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester

from kubetester.mongotester import ReplicaSetTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.helm import helm_install_from_chart
from kubetester.ldap import OpenLDAP, LDAPUser


@fixture(scope="module")
def replica_set(
    openldap_tls: OpenLDAP, issuer_ca_configmap: str, namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace
    )

    bind_query_password_secret = "bind-query-password"
    KubernetesTester.create_secret(
        namespace, bind_query_password_secret, {"password": openldap_tls.admin_password}
    )

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": openldap_tls.servers,
        "bindQueryPasswordSecretRef": {"name": bind_query_password_secret},
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "transportSecurity": "tls",
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }

    return resource.create()


@mark.e2e_replica_set_ldap_tls
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


# TODO: Move to a MongoDBUsers (based on KubeObject) for this user creation.
@mark.e2e_replica_set_ldap_tls
class TestAddLDAPUsers(KubernetesTester):
    """
    name: Create LDAP Users
    create_many:
      file: ldap/ldap-user.yaml
      wait_until: all_users_ready
    """

    def test_users_ready(self):
        pass

    @staticmethod
    def all_users_ready():
        ac = KubernetesTester.get_automation_config()
        return len(ac["auth"]["usersWanted"]) == 3


@mark.e2e_replica_set_ldap_tls
def test_new_mdb_users_are_created(
    replica_set: MongoDB, ldap_mongodb_users_tls: List[LDAPUser]
):
    tester = ReplicaSetTester(replica_set.name, 3)

    # We should be able to connect to the database as the
    # different users that were created.
    for ldap_user in ldap_mongodb_users_tls:
        print(
            "Trying to connect to {} with password {}".format(
                ldap_user.username, ldap_user.password
            )
        )
        tester.assert_ldap_authentication(
            ldap_user.username, ldap_user.password, attempts=10
        )
