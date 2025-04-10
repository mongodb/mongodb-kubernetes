from kubetester import create_secret, find_fixture
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
    ldap_mongodb_user: LDAPUser,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
        "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
    }

    return resource.create()


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
    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    return user.create()


@mark.e2e_replica_set_ldap_user_to_dn_mapping
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_user_to_dn_mapping
def test_create_ldap_user(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
    ac.assert_expected_users(1)


@mark.e2e_replica_set_ldap_user_to_dn_mapping
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(ldap_user_mongodb["spec"]["username"], ldap_user_mongodb.password, attempts=10)
