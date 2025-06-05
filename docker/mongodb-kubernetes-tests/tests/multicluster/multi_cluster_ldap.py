from typing import Dict, List

import kubernetes
from kubetester import create_secret, wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_static_containers
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_multi_cluster_operator_installation_config
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-replica-set-ldap"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
USER_NAME = "mms-user-1"
PASSWORD = "my-password"
LDAP_NAME = "openldap"


@fixture(scope="module")
def multi_cluster_operator_installation_config(namespace) -> Dict[str, str]:
    config = get_multi_cluster_operator_installation_config(namespace=namespace)
    config["customEnvVars"] = config["customEnvVars"] + "\&MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S=360"
    return config


@fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    # This test has always been tested with 5.0.5-ent. After trying to unify its variant and upgrading it
    # to MDB 6 we realized that our EVG hosts contain outdated docker and seccomp libraries in the host which
    # cause MDB process to exit. It might be a good idea to try uncommenting it after migrating to newer EVG hosts.
    # See https://github.com/docker-library/mongo/issues/606 for more information
    # resource.set_version(ensure_ent_version(custom_mdb_version))

    resource.set_version(ensure_ent_version("5.0.5-ent"))

    # Setting the initial clusterSpecList to more members than we need to generate
    # the certificates for all the members once the RS is scaled up.
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def mongodb_multi(
    mongodb_multi_unmarshalled: MongoDBMulti,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    server_certs: str,
    multicluster_openldap_tls: OpenLDAP,
    ldap_mongodb_agent_user: LDAPUser,
    issuer_ca_configmap: str,
) -> MongoDBMulti:
    resource = mongodb_multi_unmarshalled

    secret_name = "bind-query-password"
    create_secret(
        namespace,
        secret_name,
        {"password": multicluster_openldap_tls.admin_password},
        api_client=central_cluster_client,
    )
    ac_secret_name = "automation-config-password"
    create_secret(
        namespace,
        ac_secret_name,
        {"automationConfigPassword": ldap_mongodb_agent_user.password},
        api_client=central_cluster_client,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])

    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "enabled": True,
            "ca": multi_cluster_issuer_ca_configmap,
        },
        "authentication": {
            "enabled": True,
            "modes": ["LDAP", "SCRAM"],  # SCRAM for testing CLOUDP-229222
            "ldap": {
                "servers": [multicluster_openldap_tls.servers],
                "bindQueryUser": "cn=admin,dc=example,dc=org",
                "bindQueryPasswordSecretRef": {"name": secret_name},
                "transportSecurity": "none",  # For testing CLOUDP-229222
                "validateLDAPServerConfig": True,
                "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
                "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
                "timeoutMS": 12345,
                "userCacheInvalidationInterval": 60,
            },
            "agents": {
                "mode": "SCRAM",  # SCRAM for testing CLOUDP-189433
                "automationPasswordSecretRef": {
                    "name": ac_secret_name,
                    "key": "automationConfigPassword",
                },
                "automationUserName": ldap_mongodb_agent_user.uid,
                "automationLdapGroupDN": "cn=agents,ou=groups,dc=example,dc=org",
            },
        },
    }
    resource["spec"]["additionalMongodConfig"] = {"net": {"ssl": {"mode": "preferSSL"}}}
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.update()
    return resource


@fixture(scope="module")
def user_ldap(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    ldap_mongodb_user: LDAPUser,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
    mongodb_user = ldap_mongodb_user
    user = generic_user(
        namespace,
        username=mongodb_user.uid,
        db="$external",
        password=mongodb_user.password,
        mongodb_resource=mongodb_multi,
    )
    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )
    user.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    user.update()
    return user


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_mongodb_multi_pending(mongodb_multi: MongoDBMulti):
    """
    This function tests CLOUDP-229222. The resource needs to enter the "Pending" state and without the automatic
    recovery, it would stay like this forever (since we wouldn't push the new AC with a fix).
    """
    mongodb_multi.assert_reaches_phase(Phase.Pending, timeout=100)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_turn_tls_on_CLOUDP_229222(mongodb_multi: MongoDBMulti):
    """
    This function tests CLOUDP-229222. The user attempts to fix the AutomationConfig.
    Before updating the AutomationConfig, we need to ensure the operator pushed the wrong one to Ops Manager.
    """

    def wait_for_ac_exists() -> bool:
        ac = mongodb_multi.get_automation_config_tester().automation_config
        try:
            _ = ac["ldap"]["transportSecurity"]
            _ = ac["version"]
            return True
        except KeyError:
            return False

    wait_until(wait_for_ac_exists, timeout=200)
    current_version = mongodb_multi.get_automation_config_tester().automation_config["version"]

    def wait_for_ac_pushed() -> bool:
        ac = mongodb_multi.get_automation_config_tester().automation_config
        try:
            transport_security = ac["ldap"]["transportSecurity"]
            new_version = ac["version"]
            if transport_security != "none":
                return False
            if new_version <= current_version:
                return False
            return True
        except KeyError:
            return False

    wait_until(wait_for_ac_pushed, timeout=500)

    resource = mongodb_multi.load()

    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    resource.update()


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_multi_replicaset_CLOUDP_229222(mongodb_multi: MongoDBMulti):
    """
    This function tests CLOUDP-229222.  The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
    goes into running state.
    """
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1900)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_restore_mongodb_multi_ldap_configuration(mongodb_multi: MongoDBMulti):
    """
    This function restores the initial desired security configuration to carry on with the next tests normally.
    """
    resource = mongodb_multi.load()

    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP"]
    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    resource["spec"]["security"]["authentication"]["agents"]["mode"] = "LDAP"

    resource.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_create_ldap_user(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser):
    user_ldap.assert_reaches_phase(Phase.Updated)
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
    ac.assert_expected_users(1)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_ldap_user_created_and_can_authenticate(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        attempts=10,
    )


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_ops_manager_state_correctly_updated(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser):
    expected_roles = {
        ("admin", "clusterAdmin"),
        ("admin", "readWriteAnyDatabase"),
        ("admin", "dbAdminAnyDatabase"),
    }
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_expected_users(1)
    ac.assert_has_user(user_ldap["spec"]["username"])
    ac.assert_user_has_roles(user_ldap["spec"]["username"], expected_roles)
    ac.assert_authentication_mechanism_enabled("PLAIN", active_auth_mechanism=True)
    ac.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=1)

    assert "userCacheInvalidationInterval" in ac.automation_config["ldap"]
    assert "timeoutMS" in ac.automation_config["ldap"]
    assert ac.automation_config["ldap"]["userCacheInvalidationInterval"] == 60
    assert ac.automation_config["ldap"]["timeoutMS"] == 12345


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_deployment_is_reachable_with_ldap_agent(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable()


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti, member_cluster_names):
    mongodb_multi.reload()
    mongodb_multi["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_new_ldap_user_can_authenticate_after_scaling(
    mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        tls_ca_file=ca_path,
        attempts=10,
    )


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_disable_agent_auth(mongodb_multi: MongoDBMulti):
    mongodb_multi.reload()
    mongodb_multi["spec"]["security"]["authentication"]["enabled"] = False
    mongodb_multi["spec"]["security"]["authentication"]["agents"]["enabled"] = False
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_mongodb_multi_connectivity_with_no_auth(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap
def test_deployment_is_reachable_with_no_auth(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable()
