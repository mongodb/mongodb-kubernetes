import time
from typing import List

from kubernetes import client
from pytest import mark, fixture

from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.automation_config_tester import AutomationConfigTester

from kubetester.certs import create_tls_certs
from kubetester.mongotester import ReplicaSetTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.helm import helm_install_from_chart
from kubetester.ldap import OpenLDAP, LDAPUser


USER_NAME = "mms-user-1"
PASSWORD = "my-password"
MDB_RESOURCE = "ldap-replica-set"


@fixture("module")
def server_certs(namespace: str, issuer: str):
    return create_tls_certs(issuer, namespace, "ldap-replica-set", "server-certs")


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP, issuer_ca_configmap: str, server_certs: str, namespace: str
) -> MongoDB:
    bind_query_password_secret = "bind-query-password"
    resource = MongoDB.from_yaml(
        yaml_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace
    )

    KubernetesTester.create_secret(
        namespace, bind_query_password_secret, {"password": openldap.admin_password}
    )

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": openldap.servers,
        "bindQueryPasswordSecretRef": {"name": bind_query_password_secret,},
    }
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM", "X509"]
    resource["spec"]["security"]["tls"] = {
        "enabled": True,
        "ca": issuer_ca_configmap,
        "secretRef": {"name": server_certs},
    }

    return resource.create()


@mark.e2e_replica_set_ldap
def test_replica_set(replica_set: MongoDB, ldap_mongodb_users: List[LDAPUser]):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


# TODO: Move to a MongoDBUsers (based on KubeObject) for this user creation.
@mark.e2e_replica_set_ldap
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


@mark.e2e_replica_set_ldap
def test_new_mdb_users_are_created(
    replica_set: MongoDB, ldap_mongodb_users: List[LDAPUser], ca_path: str
):
    # the users take a bit to be added to the servers.
    # TODO: remove this sleep
    time.sleep(30)
    tester = ReplicaSetTester(replica_set.name, 3)

    # We should be able to connect to the database as the
    # different users that were created.
    for ldap_user in ldap_mongodb_users:
        print(
            "Trying to connect to {} with password {}".format(
                ldap_user.username, ldap_user.password
            )
        )
        tester.assert_ldap_authentication(
            ldap_user.username, ldap_user.password, ca_path
        )


@mark.e2e_replica_set_ldap
class TestCreateScramShaMongoDBUser(KubernetesTester):
    """
    description: |
      Creates a ScramSha Authenticated MongoDBUser
    create:
      file: scram-sha-user-ldap.yaml
      wait_until: all_users_ready
      timeout: 150
    """

    @classmethod
    def setup_class(cls):
        print(
            "creating password for MongoDBUser mms-user-1 in secret/mms-user-1-password"
        )
        KubernetesTester.create_secret(
            KubernetesTester.get_namespace(),
            "mms-user-1-password",
            {"password": PASSWORD,},
        )
        super().setup_class()

    @staticmethod
    def all_users_ready():
        ac = KubernetesTester.get_automation_config()
        return len(ac["auth"]["usersWanted"]) == 4

    def test_replica_set_connectivity(self, ca_path: str):
        ReplicaSetTester(
            "ldap-replica-set", 3, ssl=True, insecure=False, ca_path=ca_path
        ).assert_connectivity()

    def test_ops_manager_state_correctly_updated(self):
        expected_roles = {
            ("admin", "clusterAdmin"),
            ("admin", "userAdminAnyDatabase"),
            ("admin", "readWrite"),
            ("admin", "userAdminAnyDatabase"),
        }

        tester = AutomationConfigTester(
            KubernetesTester.get_automation_config(),
            expected_users=4,
            authoritative_set=True,
        )
        tester.assert_has_user(USER_NAME)
        tester.assert_user_has_roles(USER_NAME, expected_roles)

        # The following test should be moved to the start of the test.
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=3)

    def test_user_cannot_authenticate_with_incorrect_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication_fails(
            password="invalid-password",
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            ssl_ca_certs=ca_path,
        )

    def test_user_can_authenticate_with_correct_password(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_scram_sha_authentication(
            password=PASSWORD,
            username="mms-user-1",
            auth_mechanism="SCRAM-SHA-256",
            ssl=True,
            ssl_ca_certs=ca_path,
        )


@mark.e2e_replica_set_ldap
class TestCreateX509MongoDBUser(KubernetesTester):
    """
    description: |
      Creates a x509 Authenticated MongoDBUser
    create:
      file: test-x509-user.yaml
      patch: '[{"op": "replace", "path": "/spec/mongodbResourceRef/name", "value": "ldap-replica-set" }]'
      wait_until: user_exists
    """

    def test_add_user(self):
        assert True

    @staticmethod
    def user_exists():
        ac = KubernetesTester.get_automation_config()
        users = ac["auth"]["usersWanted"]
        return "CN=x509-testing-user" in [user["user"] for user in users]


@mark.e2e_replica_set_ldap
class TestCreateX509MongoDBUserCreatesClientCert(KubernetesTester):
    def setup(self):
        cert_name = "x509-testing-user." + self.get_namespace()
        self.cert_file = self.generate_certfile(
            cert_name, "x509-testing-user.csr", "server-key.pem"
        )

    def teardown(self):
        self.cert_file.close()

    def test_can_authenticate_with_added_user(self, ca_path: str):
        tester = ReplicaSetTester(MDB_RESOURCE, 3)
        tester.assert_x509_authentication(
            cert_file_name=self.cert_file.name, ssl_ca_certs=ca_path
        )
