from typing import Dict

from pytest import mark, fixture

from kubetester import find_fixture, create_or_update_secret, create_or_update
from kubetester.ldap import OpenLDAP, LDAPUser, LDAP_AUTHENTICATION_MECHANISM
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, generic_user, Role


@fixture(scope="module")
def operator_installation_config(
    operator_installation_config: Dict[str, str]
) -> Dict[str, str]:
    """
    This functions appends automatic recovery settings for CLOUDP-189433. In order to make the test runnable in reasonable time,
    we override the Recovery back off to 10 seconds only. This way it immediately kicks in.
    """
    operator_installation_config["customEnvVars"] = (
        operator_installation_config["customEnvVars"]
        + "\&MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S=10"
    )
    return operator_installation_config


@fixture(scope="module")
def replica_set(
    openldap_tls: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
) -> MongoDB:
    """
    This function sets up ReplicaSet resource for testing. The testing procedure includes CLOUDP-189433, that requires
    putting the resource into "Pending" state and the automatically recovering it.
    """
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace
    )

    secret_name = "bind-query-password"
    create_or_update_secret(
        namespace, secret_name, {"password": openldap_tls.admin_password}
    )

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "transportSecurity": "none",  # For testing CLOUDP-189433
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }

    create_or_update(resource)
    return resource


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
def test_replica_set_pending_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The resource needs to enter the "Pending" state and without the automatic
    recovery, it would stay like this forever (since we wouldn't push the new AC with a fix).
    """
    replica_set.assert_reaches_phase(Phase.Pending, timeout=100)


@mark.e2e_replica_set_ldap_tls
def test_turn_tls_on_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The user attempts to fix the AutomationConfig.
    """
    resource = replica_set.load()
    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    create_or_update(resource)


@mark.e2e_replica_set_ldap_tls
def test_replica_set_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
    goes into running state.
    """
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


@mark.e2e_replica_set_ldap_tls
def test_remove_ldap_settings(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["ldap"] = None
    replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)
