import os
from pytest import mark, fixture
from typing import List

import kubernetes
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester import get_pod_when_ready, create_secret, create_or_update
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.ldap import (
    OpenLDAP,
    LDAPUser,
    LDAP_AUTHENTICATION_MECHANISM,
)
from kubetester.helm import helm_install
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongodb_user import MongoDBUser, generic_user, Role
from kubetester.operator import Operator
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from tests.multicluster.conftest import cluster_spec_list
from tests.opsmanager.conftest import ensure_ent_version

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-replica-set-ldap"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
USER_NAME = "mms-user-1"
PASSWORD = "my-password"
LDAP_NAME = "openldap"


@fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))

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
            "modes": ["LDAP"],
            "ldap": {
                "servers": [multicluster_openldap_tls.servers],
                "bindQueryUser": "cn=admin,dc=example,dc=org",
                "bindQueryPasswordSecretRef": {"name": secret_name},
                "transportSecurity": "tls",
                "validateLDAPServerConfig": True,
                "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
                "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
            },
            "agents": {
                "mode": "LDAP",
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

    create_or_update(resource)
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
    create_or_update(user)
    return user


@mark.e2e_multi_cluster_with_ldap
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_with_ldap
def test_create_mongodb_multi_with_ldap(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_with_ldap
def test_create_ldap_user(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser):
    user_ldap.assert_reaches_phase(Phase.Updated)
    ac = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=True)
    ac.assert_expected_users(1)


@mark.e2e_multi_cluster_with_ldap
def test_ldap_user_created_and_can_authenticate(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        ssl_ca_certs=ca_path,
        attempts=10,
    )


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


@mark.e2e_multi_cluster_with_ldap
def test_deployment_is_reachable_with_ldap_agent(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable(attempts=10)


@mark.e2e_multi_cluster_with_ldap
def test_scale_mongodb_multi(mongodb_multi: MongoDBMulti, member_cluster_names):
    mongodb_multi.reload()
    mongodb_multi["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_multi_cluster_with_ldap
def test_new_ldap_user_can_authenticate_after_scaling(
    mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str
):
    tester = mongodb_multi.tester()
    tester.assert_ldap_authentication(
        username=user_ldap["spec"]["username"],
        password=user_ldap.password,
        ssl_ca_certs=ca_path,
        attempts=10,
    )


@mark.e2e_multi_cluster_with_ldap
def test_disable_agent_auth(mongodb_multi: MongoDBMulti):
    mongodb_multi.reload()
    mongodb_multi["spec"]["security"]["authentication"]["enabled"] = False
    mongodb_multi["spec"]["security"]["authentication"]["agents"]["enabled"] = False
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_with_ldap
def test_mongodb_multi_connectivity_with_no_auth(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@mark.e2e_multi_cluster_with_ldap
def test_deployment_is_reachable_with_no_auth(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_deployment_reachable(attempts=10)
