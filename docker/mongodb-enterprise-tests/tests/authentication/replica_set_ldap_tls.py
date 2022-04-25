from pytest import mark, fixture

from kubetester import create_secret, find_fixture

from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, generic_user, Role
from kubetester.ldap import OpenLDAP, LDAPUser, LDAP_AUTHENTICATION_MECHANISM


@fixture(scope="module")
def replica_set(
    openldap_tls: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace
    )

    secret_name = "bind-query-password"
    create_secret(namespace, secret_name, {"password": openldap_tls.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "transportSecurity": "tls",
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }

    return resource.create()


@fixture(scope="module")
def ldap_user_mongodb(
    replica_set: MongoDB, namespace: str, ldap_mongodb_user_tls: LDAPUser
) -> MongoDBUser:
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


@mark.e2e_replica_set_ldap_tls
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_tls
def test_create_ldap_user(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled(
        LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False
    )
    ac.assert_expected_users(1)


@mark.e2e_replica_set_ldap_tls
def test_new_ldap_users_can_authenticate(
    replica_set: MongoDB, ldap_user_mongodb: MongoDBUser
):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        ldap_user_mongodb["spec"]["username"], ldap_user_mongodb.password, attempts=10
    )
