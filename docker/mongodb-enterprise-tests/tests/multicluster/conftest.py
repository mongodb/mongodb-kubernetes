#!/usr/bin/env python3
from typing import Generator, List
from kubetester.ldap import (
    OpenLDAP,
    LDAPUser,
    create_user,
    ensure_organizational_unit,
    ensure_group,
    ensure_organization,
    add_user_to_group,
)

from kubetester.mongodb_multi import MultiClusterClient
from kubetester.certs import generate_cert
from pytest import fixture
from tests.authentication.conftest import (
    openldap_install,
    LDAP_NAME,
    LDAP_PASSWORD,
    AUTOMATION_AGENT_NAME,
    ldap_host,
)
from kubernetes import client
from kubernetes.client import V1ObjectMeta


@fixture(scope="module")
def multicluster_openldap_cert(
    member_cluster_clients: List[MultiClusterClient], multi_cluster_ldap_issuer: str
) -> str:
    """Returns a new secret to be used to enable TLS on LDAP."""

    member_cluster_one = member_cluster_clients[0]
    # create openldap namespace if it doesn't exist
    ns = client.V1Namespace(metadata=V1ObjectMeta(name="openldap"))

    try:
        client.CoreV1Api(api_client=member_cluster_one.api_client).create_namespace(ns)
    except client.rest.ApiException as e:
        if e.status == 409:
            print("Namespace openldap already exists")

    host = ldap_host("openldap", LDAP_NAME)
    return generate_cert(
        "openldap",
        "openldap",
        host,
        multi_cluster_ldap_issuer,
        api_client=member_cluster_one.api_client,
    )


@fixture(scope="module")
def multicluster_openldap_tls(
    member_cluster_clients: List[MultiClusterClient],
    multicluster_openldap_cert: str,
    namespace: str,
) -> Generator[OpenLDAP, None, None]:
    member_cluster_one = member_cluster_clients[0]
    helm_args = {
        "tls.enabled": "true",
        "tls.secret": multicluster_openldap_cert,
        # Do not require client certificates
        "env.LDAP_TLS_VERIFY_CLIENT": "never",
        "namespace": namespace,
    }
    return openldap_install(
        "openldap",
        LDAP_NAME,
        helm_args=helm_args,
        cluster_client=member_cluster_one.api_client,
        cluster_name=member_cluster_one.cluster_name,
        tls=True,
    )


@fixture(scope="module")
def ldap_mongodb_user(multicluster_openldap_tls: OpenLDAP, ca_path: str) -> LDAPUser:
    user = LDAPUser("mdb0", LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap_tls, "groups", ca_path=ca_path)
    create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)

    ensure_group(multicluster_openldap_tls, cn="users", ou="groups", ca_path=ca_path)
    add_user_to_group(
        multicluster_openldap_tls,
        user="mdb0",
        group_cn="users",
        ou="groups",
        ca_path=ca_path,
    )

    return user


@fixture(scope="module")
def ldap_mongodb_users(
    multicluster_openldap_tls: OpenLDAP, ca_path: str
) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(multicluster_openldap_tls, user, ca_path=ca_path)
    return user_list


@fixture(scope="module")
def ldap_mongodb_agent_user(
    multicluster_openldap_tls: OpenLDAP, ca_path: str
) -> LDAPUser:
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap_tls, "groups", ca_path=ca_path)
    create_user(multicluster_openldap_tls, user, ou="groups", ca_path=ca_path)

    ensure_group(multicluster_openldap_tls, cn="agents", ou="groups", ca_path=ca_path)
    add_user_to_group(
        multicluster_openldap_tls,
        user=AUTOMATION_AGENT_NAME,
        group_cn="agents",
        ou="groups",
        ca_path=ca_path,
    )

    return user
