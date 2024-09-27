from kubetester import (
    create_or_update,
    create_or_update_secret,
    create_secret,
    find_fixture,
    try_load,
)
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from pytest import fixture, mark
from tests.opsmanager.conftest import ensure_ent_version

USER_NAME = "mms-user-1"
PASSWORD = "my-password"


@fixture(scope="module")
def replica_set(openldap: OpenLDAP, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-agent-auth.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})

    ac_secret_name = "automation-config-password"
    create_secret(namespace, ac_secret_name, {"automationConfigPassword": "LDAPPassword."})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
    }
    resource["spec"]["security"]["authentication"]["agents"] = {
        "mode": "LDAP",
        "automationPasswordSecretRef": {
            "name": ac_secret_name,
            "key": "automationConfigPassword",
        },
        "automationUserName": "mms-automation-agent",
    }
    resource.set_version("4.4.4-ent")

    # we need to fix this, but this test only works when upgrading from 4.4.4 to 5.0.14
    resource.set_version(ensure_ent_version("4.4.4-ent"))

    try_load(resource)

    return resource


@fixture(scope="module")
def ldap_user_mongodb(replica_set: MongoDB, namespace: str, ldap_mongodb_user: LDAPUser) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user.uid,
        db="$external",
        mongodb_resource=replica_set,
        password=ldap_mongodb_user.password,
    )
    user.add_roles(
        [
            # In order to be able to write to custom db/collections during the tests
            Role(db="admin", role="readWriteAnyDatabase"),
        ]
    )

    return user.create()


@mark.e2e_replica_set_ldap_agent_auth
@mark.usefixtures("ldap_mongodb_agent_user", "ldap_user_mongodb")
def test_replica_set(replica_set: MongoDB):
    create_or_update(replica_set)
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_agent_auth
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_auth
def test_deployment_is_reachable_with_ldap_agent(replica_set: MongoDB):
    tester = replica_set.tester()
    # Due to what we found out in
    # https://jira.mongodb.org/browse/CLOUDP-68873
    # the agents might report being in goal state, the MDB resource
    # would report no errors but the deployment would be unreachable
    # See the comment inside the function for further details
    tester.assert_deployment_reachable()


@mark.e2e_replica_set_ldap_agent_auth
def test_scale_replica_test(replica_set: MongoDB):
    replica_set.reload()
    replica_set["spec"]["members"] = 5
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_ldap_agent_auth
def test_new_ldap_users_can_authenticate_after_scaling(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="customDb",
        collection="customColl",
        attempts=10,
    )


@mark.e2e_replica_set_ldap_agent_auth
def test_disable_agent_auth(replica_set: MongoDB):
    replica_set.reload()
    replica_set["spec"]["security"]["authentication"]["enabled"] = False
    replica_set["spec"]["security"]["authentication"]["agents"]["enabled"] = False
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_replica_set_ldap_agent_auth
def test_replica_set_connectivity_with_no_auth(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_connectivity()


@mark.e2e_replica_set_ldap_agent_auth
def test_deployment_is_reachable_with_no_auth(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_deployment_reachable()


@mark.e2e_replica_set_ldap_agent_auth
def test_replica_set_connectivity_after_version_change_no_auth(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_connectivity()


@mark.e2e_replica_set_ldap_agent_auth
def test_deployment_is_reachable_after_version_change(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_deployment_reachable()


@mark.e2e_replica_set_ldap_agent_auth
def test_enable_SCRAM_auth(replica_set: MongoDB):
    replica_set["spec"]["security"]["authentication"]["agents"]["enabled"] = True
    replica_set["spec"]["security"]["authentication"]["agents"]["mode"] = "SCRAM"
    replica_set["spec"]["security"]["authentication"]["enabled"] = True
    replica_set["spec"]["security"]["authentication"]["mode"] = "SCRAM"
    create_or_update(replica_set)
    replica_set.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_replica_set_ldap_agent_auth
def test_replica_set_connectivity_with_SCRAM_auth(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_connectivity()


@mark.e2e_replica_set_ldap_agent_auth
def test_change_version_to_latest(replica_set: MongoDB):
    replica_set.reload()
    replica_set.set_version(ensure_ent_version("5.0.14"))
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_replica_set_ldap_agent_auth
def test_replica_set_connectivity_after_version_change_SCRAM(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_connectivity()


@mark.e2e_replica_set_ldap_agent_auth
def test_deployment_is_reachable_after_version_change_SCRAM(replica_set: MongoDB):
    tester = replica_set.tester()
    tester.assert_deployment_reachable()
