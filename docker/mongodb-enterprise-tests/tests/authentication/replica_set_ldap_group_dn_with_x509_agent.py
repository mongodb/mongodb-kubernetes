import random

from pytest import mark, fixture

from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import create_secret, find_fixture
from kubetester import kubetester

from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser, generic_user, Role
from kubetester.ldap import OpenLDAP, LDAPUser, LDAP_AUTHENTICATION_MECHANISM
from kubetester.omtester import get_rs_cert_names

from datetime import datetime, timezone
import time


@fixture(scope="module")
def replica_set(
    openldap: OpenLDAP, issuer_ca_configmap: str, namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("ldap/ldap-agent-auth.yaml"), namespace=namespace
    )

    secret_name = "bind-query-password"
    create_secret(secret_name, namespace, {"password": openldap.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap.servers],
        "bindQueryUser": "cn=admin,dc=example,dc=org",
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
        "userToDNMapping": '[{match: "CN=mms-automation-agent,(.+),L=NY,ST=NY,C=US", substitution: "uid=mms-automation-agent,{0},dc=example,dc=org"}, {match: "(.+)", substitution:"uid={0},ou=groups,dc=example,dc=org"}]',
        "authzQueryTemplate": "{USER}?memberOf?base",
    }

    resource["spec"]["security"]["tls"] = {"enabled": True}
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
def ldap_user_mongodb(
    replica_set: MongoDB, namespace: str, ldap_mongodb_user: LDAPUser
) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user.uid,
        db="$external",
        mongodb_resource=replica_set,
        password=ldap_mongodb_user.password,
    )

    return user.create()


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_replica_set(
    replica_set: MongoDB, ldap_mongodb_x509_agent_user: LDAPUser, namespace: str
):

    certs = get_rs_cert_names(
        replica_set["metadata"]["name"], namespace, with_agent_certs=True
    )

    timeout = 300
    stop_time = time.time() + timeout
    while len(certs) > 0 and time.time() < stop_time:
        # We randomly choose a cert from the list instead of proceeding sequentially
        # in case some certs are generated only after others are approved.
        # This way it is a bit more stable in case the order of items change,
        # otherwise we might got stuck on a single cert that has not appeared yet in K8S.
        cert_name = random.choice(certs)
        try:
            body = client.CertificatesV1beta1Api().read_certificate_signing_request_status(
                cert_name
            )
            conditions = client.V1beta1CertificateSigningRequestCondition(
                last_update_time=datetime.now(timezone.utc).astimezone(),
                message="This certificate was approved by E2E testing framework",
                reason="E2ETestingFramework",
                type="Approved",
            )

            body.status.conditions = [conditions]
            client.CertificatesV1beta1Api().replace_certificate_signing_request_approval(
                cert_name, body
            )
            certs.remove(cert_name)
        except ApiException:
            time.sleep(5)
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_new_ldap_users_can_authenticate(
    replica_set: MongoDB, ldap_user_mongodb: MongoDBUser
):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(
        username=ldap_user_mongodb["spec"]["username"],
        password=ldap_user_mongodb.password,
        db="foo",
        collection="foo",
        attempts=10,
        ssl_ca_certs=kubetester.SSL_CA_CERT,
    )


@mark.e2e_replica_set_ldap_group_dn_with_x509_agent
def test_deployment_is_reachable_with_ldap_agent(replica_set: MongoDB):
    tester = replica_set.tester()
    # Due to what we found out in
    # https://jira.mongodb.org/browse/CLOUDP-68873
    # the agents might report being in goal state, the MDB resource
    # would report no errors but the deployment would be unreachable
    # See the comment inside the function for further details
    tester.assert_deployment_reachable(attempts=10)
