from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi_with_ldap(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_create_ldap_user(mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser):
    user_ldap.assert_reaches_phase(Phase.Updated)
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
    ac.assert_expected_users(1)


def test_ldap_user_can_write_to_database(mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        db="foo",
        collection="foo",
        attempts=10,
    )


def test_ldap_user_can_write_to_other_collection(
    mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        db="foo",
        collection="foo2",
        attempts=10,
    )


def test_ldap_user_can_write_to_other_database(
    mongodb_multi: MongoDBMulti | MongoDB, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        db="foo2",
        collection="foo",
        attempts=10,
    )


def test_automation_config_has_roles(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.get_automation_config_tester()
    role = {
        "role": "cn=users,ou=groups,dc=example,dc=org",
        "db": "admin",
        "privileges": [
            {"actions": ["insert"], "resource": {"collection": "foo", "db": "foo"}},
            {
                "actions": ["insert", "find"],
                "resource": {"collection": "", "db": "admin"},
            },
        ],
        "authenticationRestrictions": [],
    }
    tester.assert_expected_role(role_index=0, expected_value=role)
