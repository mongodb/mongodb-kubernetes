import os
from typing import List, Generator, Optional

from kubernetes import client
from kubetester import get_pod_when_ready, read_secret
from kubetester.certs import generate_cert
from kubetester.helm import helm_install, helm_uninstall
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.ldap import (
    create_user,
    ensure_organizational_unit,
    ensure_group,
    ensure_organization,
    add_user_to_group,
    OpenLDAP,
    LDAPUser,
)
from pytest import fixture

LDAP_PASSWORD = "LDAPPassword."
LDAP_NAME = "openldap"
LDAP_POD_LABEL = "app=openldap"
LDAP_PORT_PLAIN = 389
LDAP_PORT_TLS = 636
LDAP_PROTO_PLAIN = "ldap"
LDAP_PROTO_TLS = "ldaps"

AUTOMATION_AGENT_NAME = "mms-automation-agent"


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test"""
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")


def openldap_install(
    namespace: str,
    name: str = LDAP_NAME,
    cluster_client: Optional[client.ApiClient] = None,
    cluster_name: Optional[str] = None,
) -> OpenLDAP:
    if cluster_name is not None:
        os.environ["HELM_KUBECONTEXT"] = cluster_name
    helm_install(
        name=name,
        namespace=namespace,
        helm_args={
            "namespace": namespace,
            "fullnameOverride": name,
            "nameOverride": name,
        },
        helm_chart_path="vendor/openldap",
    )
    get_pod_when_ready(namespace, f"app={name}", api_client=cluster_client)

    return OpenLDAP(
        ldap_url(namespace, name),
        ldap_admin_password(namespace, name, api_client=cluster_client),
    )


@fixture(scope="module")
def openldap(namespace: str) -> Generator[OpenLDAP, None, None]:
    """Installs a OpenLDAP server and returns a reference to it.

    In order to do it, this fixture will install the vendored openldap Helm chart
    located in `vendor/openldap` directory inside the `tests` container image.
    """
    yield openldap_install(namespace, LDAP_NAME)

    helm_uninstall(LDAP_NAME)


@fixture(scope="module")
def secondary_openldap(namespace: str) -> Generator[OpenLDAP, None, None]:
    yield openldap_install(namespace, f"{LDAP_NAME}secondary")

    helm_uninstall(f"{LDAP_NAME}secondary")


@fixture(scope="module")
def openldap_cert(namespace: str, issuer: str) -> str:
    """Returns a new secret to be used to enable TLS on LDAP."""
    host = ldap_host(namespace, LDAP_NAME)
    return generate_cert(namespace, "openldap", host, issuer)


@fixture(scope="module")
def openldap_tls(namespace: str, openldap_cert: str) -> Generator[OpenLDAP, None, None]:
    """Installs an OpenLDAP server with TLS configured and returns a reference to it.

    In order to do it, this fixture will install the vendored openldap Helm chart
    located in `vendor/openldap` directory inside the `tests` container image.
    """
    helm_args = {
        "tls.enabled": "true",
        "tls.secret": openldap_cert,
        # Do not require client certificates
        "env.LDAP_TLS_VERIFY_CLIENT": "never",
        "namespace": namespace,
    }
    helm_install(
        name=LDAP_NAME,
        namespace=namespace,
        helm_chart_path="vendor/openldap",
        helm_args=helm_args,
    )

    pod = get_pod_when_ready(namespace, LDAP_POD_LABEL)

    yield OpenLDAP(
        ldap_url(namespace, LDAP_NAME, LDAP_PROTO_TLS, LDAP_PORT_TLS),
        ldap_admin_password(namespace, LDAP_NAME),
    )

    helm_uninstall(LDAP_NAME)


@fixture(scope="module")
def ldap_mongodb_user_tls(openldap_tls: OpenLDAP, ca_path: str) -> LDAPUser:
    user = LDAPUser("mdb0", LDAP_PASSWORD)
    create_user(openldap_tls, user, ca_path=ca_path)

    return user


@fixture(scope="module")
def ldap_mongodb_x509_agent_user(
    openldap: OpenLDAP, namespace: str, ca_path: str
) -> LDAPUser:
    organization_name = "cluster.local-agent"
    user = LDAPUser(
        AUTOMATION_AGENT_NAME,
        LDAP_PASSWORD,
    )

    ensure_organization(openldap, organization_name, ca_path=ca_path)

    ensure_organizational_unit(
        openldap, namespace, o=organization_name, ca_path=ca_path
    )
    create_user(openldap, user, ou=namespace, o=organization_name)

    ensure_group(
        openldap,
        cn=AUTOMATION_AGENT_NAME,
        ou=namespace,
        o=organization_name,
        ca_path=ca_path,
    )

    add_user_to_group(
        openldap,
        user=AUTOMATION_AGENT_NAME,
        group_cn=AUTOMATION_AGENT_NAME,
        ou=namespace,
        o=organization_name,
    )
    return user


@fixture(scope="module")
def ldap_mongodb_agent_user(openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD)

    ensure_organizational_unit(openldap, "groups")
    create_user(openldap, user, ou="groups")

    ensure_group(openldap, cn="agents", ou="groups")
    add_user_to_group(
        openldap, user=AUTOMATION_AGENT_NAME, group_cn="agents", ou="groups"
    )

    return user


@fixture(scope="module")
def secondary_ldap_mongodb_agent_user(secondary_openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD)

    ensure_organizational_unit(secondary_openldap, "groups")
    create_user(secondary_openldap, user, ou="groups")

    ensure_group(secondary_openldap, cn="agents", ou="groups")
    add_user_to_group(
        secondary_openldap, user=AUTOMATION_AGENT_NAME, group_cn="agents", ou="groups"
    )

    return user


@fixture(scope="module")
def ldap_mongodb_user(openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser("mdb0", LDAP_PASSWORD)

    ensure_organizational_unit(openldap, "groups")
    create_user(openldap, user, ou="groups")

    ensure_group(openldap, cn="users", ou="groups")
    add_user_to_group(openldap, user="mdb0", group_cn="users", ou="groups")

    return user


@fixture(scope="module")
def ldap_mongodb_users(openldap: OpenLDAP) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(openldap, user)

    return user_list


@fixture(scope="module")
def secondary_ldap_mongodb_users(secondary_openldap: OpenLDAP) -> List[LDAPUser]:
    user_list = [LDAPUser("mdb0", LDAP_PASSWORD)]
    for user in user_list:
        create_user(secondary_openldap, user)

    return user_list


def ldap_host(namespace: str, name: str) -> str:
    return "{}.{}.svc.cluster.local".format(name, namespace)


def ldap_url(
    namespace: str,
    name: str,
    proto: str = LDAP_PROTO_PLAIN,
    port: int = LDAP_PORT_PLAIN,
) -> str:
    host = ldap_host(namespace, name)
    return "{}://{}:{}".format(proto, host, port)


def ldap_admin_password(
    namespace: str, name: str, api_client: Optional[client.ApiClient] = None
) -> str:
    return read_secret(namespace, name, api_client=api_client)["LDAP_ADMIN_PASSWORD"]
