from kubetester import create_secret, find_fixture, wait_until
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, generic_user
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
    ldap_mongodb_user: LDAPUser,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-replica-set-roles.yaml"), namespace=namespace)

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

    resource.create()
    return resource
    resource.delete()


@fixture(scope="module")
def ldap_user_mongodb(
    replica_set: MongoDB,
    namespace: str,
    ldap_mongodb_user: LDAPUser,
    openldap: OpenLDAP,
) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user.uid,
        db="$external",
        mongodb_resource=replica_set,
        password=ldap_mongodb_user.password,
    )

    yield user.create()
    user.delete()


@mark.e2e_replica_set_update_roles_no_privileges
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_update_roles_no_privileges
def test_create_ldap_user(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
    ac.assert_expected_users(1)


@mark.e2e_replica_set_update_roles_no_privileges
def test_new_ldap_users_can_write_to_database(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="foo",
        collection="foo",
        attempts=10,
    )


@mark.e2e_replica_set_update_roles_no_privileges
def test_automation_config_has_roles(replica_set: MongoDB):
    tester = replica_set.get_automation_config_tester()

    tester.assert_has_expected_number_of_roles(expected_roles=1)
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


@mark.e2e_replica_set_update_roles_no_privileges
def test_update_role(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"]["roles"] = [
        {
            "db": "admin",
            "role": "cn=users,ou=groups,dc=example,dc=org",
            "roles": [{"db": "admin", "role": "readWriteAnyDatabase"}],
        }
    ]
    replica_set.update()


@mark.e2e_replica_set_update_roles_no_privileges
def test_automation_config_has_new_roles(replica_set: MongoDB):
    role = {
        "role": "cn=users,ou=groups,dc=example,dc=org",
        "db": "admin",
        "privileges": [],
        "roles": [{"db": "admin", "role": "readWriteAnyDatabase"}],
        "authenticationRestrictions": [],
    }

    def has_role() -> bool:
        tester = replica_set.get_automation_config_tester()
        return tester.get_role_at_index(0) == role

    wait_until(has_role, timeout=90, sleep_time=5)
