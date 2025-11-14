from typing import List

import kubernetes
from kubetester import create_secret
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_static_containers
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser, generic_user
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_ldap_custom_roles as testhelper

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-replica-set-ldap"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)

    # This test has always been tested with 5.0.5-ent. After trying to unify its variant and upgrading it
    # to MDB 6 we realized that our EVG hosts contain outdated docker and seccomp libraries in the host which
    # cause MDB process to exit. It might be a good idea to try uncommenting it after migrating to newer EVG hosts.
    # See https://github.com/docker-library/mongo/issues/606 for more information
    # resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_version("5.0.5-ent")

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

    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "enabled": True,
            "ca": multi_cluster_issuer_ca_configmap,
        },
        "authentication": {
            "enabled": True,
            "modes": ["LDAP", "SCRAM"],
            "ldap": {
                "servers": [multicluster_openldap_tls.servers],
                "bindQueryUser": "cn=admin,dc=example,dc=org",
                "bindQueryPasswordSecretRef": {"name": secret_name},
                "transportSecurity": "tls",
                "validateLDAPServerConfig": True,
                "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
                "userToDNMapping": '[{match: "(.+)",substitution: "uid={0},ou=groups,dc=example,dc=org"}]',
                "authzQueryTemplate": "{USER}?memberOf?base",
            },
            "agents": {
                "mode": "SCRAM",
            },
        },
        "roles": [
            {
                "role": "cn=users,ou=groups,dc=example,dc=org",
                "db": "admin",
                "privileges": [
                    {
                        "actions": ["insert"],
                        "resource": {"collection": "foo", "db": "foo"},
                    },
                    {
                        "actions": ["insert", "find"],
                        "resource": {"collection": "", "db": "admin"},
                    },
                ],
            },
        ],
    }
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
    user.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    user.update()
    return user


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
def test_create_mongodb_multi_with_ldap(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi_with_ldap(mongodb_multi)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
def test_create_ldap_user(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser):
    testhelper.test_create_ldap_user(mongodb_multi, user_ldap)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
def test_ldap_user_can_write_to_database(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str):
    testhelper.test_ldap_user_can_write_to_database(mongodb_multi, user_ldap, ca_path)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
@mark.xfail(reason="The user should not be able to write to a database/collection it is not authorized to write on")
def test_ldap_user_can_write_to_other_collection(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str):
    testhelper.test_ldap_user_can_write_to_other_collection(mongodb_multi, user_ldap, ca_path)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
@mark.xfail(reason="The user should not be able to write to a database/collection it is not authorized to write on")
def test_ldap_user_can_write_to_other_database(mongodb_multi: MongoDBMulti, user_ldap: MongoDBUser, ca_path: str):
    testhelper.test_ldap_user_can_write_to_other_database(mongodb_multi, user_ldap, ca_path)


@skip_if_static_containers
@mark.e2e_multi_cluster_with_ldap_custom_roles
def test_automation_config_has_roles(mongodb_multi: MongoDBMulti):
    testhelper.test_automation_config_has_roles(mongodb_multi)
