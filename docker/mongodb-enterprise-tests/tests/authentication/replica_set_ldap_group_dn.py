from kubetester import create_secret, find_fixture
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, generic_user
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
    ldap_mongodb_user: LDAPUser,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-agent-auth.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
        "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
        "authzQueryTemplate": "{USER}?memberOf?base",
    }

    ac_secret_name = "automation-config-password"
    create_secret(namespace, ac_secret_name, {"automationConfigPassword": "LDAPPassword."})
    resource["spec"]["security"]["roles"] = [
        {
            "role": "cn=users,ou=groups,dc=example,dc=org",
            "db": "admin",
            "privileges": [
                {"actions": ["insert"], "resource": {"db": "foo", "collection": "foo"}},
            ],
        },
    ]
    resource["spec"]["security"]["authentication"]["agents"] = {
        "mode": "LDAP",
        "automationPasswordSecretRef": {
            "name": ac_secret_name,
            "key": "automationConfigPassword",
        },
        "automationUserName": "mms-automation-agent",
        "automationLdapGroupDN": "cn=agents,ou=groups,dc=example,dc=org",
    }
    return resource.create()


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

    return user.create()


@mark.e2e_replica_set_ldap_group_dn
def test_replica_set(
    replica_set: MongoDB,
    ldap_mongodb_agent_user: LDAPUser,
    ldap_user_mongodb: MongoDBUser,
):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_group_dn
def test_ldap_user_mongodb_reaches_updated_phase(ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated, timeout=150)


@mark.e2e_replica_set_ldap_group_dn
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="foo",
        collection="foo",
        attempts=10,
    )


@mark.e2e_replica_set_ldap_group_dn
def test_deployment_is_reachable_with_ldap_agent(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()
    # Due to what we found out in
    # https://jira.mongodb.org/browse/CLOUDP-68873
    # the agents might report being in goal state, the MDB resource
    # would report no errors but the deployment would be unreachable
    # See the comment inside the function for further details
    tester.assert_deployment_reachable()
