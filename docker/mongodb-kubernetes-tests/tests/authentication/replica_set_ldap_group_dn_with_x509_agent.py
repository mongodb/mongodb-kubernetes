import random
import time
from datetime import datetime, timezone

from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import create_secret, find_fixture, kubetester
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_x509_agent_tls_certs,
    create_x509_mongodb_tls_certs,
)
from kubetester.ldap import LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, generic_user
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "ldap-replica-set"


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE)


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP,
    issuer_ca_configmap: str,
    server_certs: str,
    agent_certs: str,
    namespace: str,
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
        "userToDNMapping": '[{match: "CN=mms-automation-agent,(.+),L=NY,ST=NY,C=US", substitution: "uid=mms-automation-agent,{0},dc=example,dc=org"}, {match: "(.+)", substitution:"uid={0},ou=groups,dc=example,dc=org"}]',
        "authzQueryTemplate": "{USER}?memberOf?base",
    }

    resource["spec"]["security"]["tls"] = {"enabled": True, "ca": issuer_ca_configmap}
    resource["spec"]["security"]["roles"] = [
        {
            "role": "cn=users,ou=groups,dc=example,dc=org",
            "db": "admin",
            "privileges": [
                {"actions": ["insert"], "resource": {"db": "foo", "collection": "foo"}},
            ],
        },
    ]
    resource["spec"]["security"]["authentication"]["modes"] = ["LDAP", "SCRAM", "X509"]
    resource["spec"]["security"]["authentication"]["agents"] = {
        "mode": "X509",
        "automationLdapGroupDN": f"cn=mms-automation-agent,ou={namespace},o=cluster.local-agent,dc=example,dc=org",
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


@fixture(scope="module")
def server_certs(issuer: str, namespace: str):
    return create_x509_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE, f"{MDB_RESOURCE}-cert")


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_replica_set(replica_set: MongoDB, ldap_mongodb_x509_agent_user: LDAPUser, namespace: str):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_ldap_user_mongodb_reaches_updated_phase(ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated, timeout=150)


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser, ca_path: str):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="foo",
        collection="foo",
        attempts=10,
        tls_ca_file=ca_path,
    )


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_deployment_is_reachable_with_ldap_agent(replica_set: MongoDB):
    tester = replica_set.tester()
    # Due to what we found out in
    # https://jira.mongodb.org/browse/CLOUDP-68873
    # the agents might report being in goal state, the MDB resource
    # would report no errors but the deployment would be unreachable
    # See the comment inside the function for further details
    tester.assert_deployment_reachable()
