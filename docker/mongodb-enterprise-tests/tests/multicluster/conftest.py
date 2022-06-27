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
from pytest import fixture
from tests.authentication.conftest import (
    openldap_install,
    LDAP_NAME,
    LDAP_PASSWORD,
    AUTOMATION_AGENT_NAME,
)


@fixture(scope="module")
def multicluster_openldap(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> Generator[OpenLDAP, None, None]:
    member_cluster_one = member_cluster_clients[0]
    yield openldap_install(
        namespace,
        LDAP_NAME,
        cluster_client=member_cluster_one.api_client,
        cluster_name=member_cluster_one.cluster_name,
    )


@fixture(scope="module")
def ldap_mongodb_user(multicluster_openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser("mdb0", LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap, "groups")
    create_user(multicluster_openldap, user, ou="groups")

    ensure_group(multicluster_openldap, cn="users", ou="groups")
    add_user_to_group(multicluster_openldap, user="mdb0", group_cn="users", ou="groups")

    return user


@fixture(scope="module")
def ldap_mongodb_users(multicluster_openldap: OpenLDAP) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(multicluster_openldap, user)
    return user_list


@fixture(scope="module")
def ldap_mongodb_agent_user(multicluster_openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD)

    ensure_organizational_unit(multicluster_openldap, "groups")
    create_user(multicluster_openldap, user, ou="groups")

    ensure_group(multicluster_openldap, cn="agents", ou="groups")
    add_user_to_group(
        multicluster_openldap,
        user=AUTOMATION_AGENT_NAME,
        group_cn="agents",
        ou="groups",
    )

    return user
