from typing import List

from kubetester import get_pod_when_ready
from kubetester.certs import generate_cert
from kubetester.helm import helm_install_from_chart, helm_uninstall
from kubetester.kubetester import KubernetesTester
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
    """ This allows to automatically install the default Operator before running any test """
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")


@fixture(scope="module")
def openldap(namespace: str) -> OpenLDAP:
    """Installs a OpenLDAP server and returns a reference to it."""
    helm_install_from_chart(namespace, LDAP_NAME, "stable/openldap", "1.2.4")

    pod = get_pod_when_ready(namespace, LDAP_POD_LABEL)

    yield OpenLDAP(ldap_url(namespace), ldap_admin_password(namespace))

    helm_uninstall(LDAP_NAME)


@fixture(scope="module")
def openldap_cert(namespace: str, issuer: str) -> str:
    """Returns a new secret to be used to enable TLS on LDAP."""
    host = ldap_host(namespace)
    return generate_cert(namespace, "openldap", host, issuer)


@fixture(scope="module")
def openldap_tls(namespace: str, openldap_cert: str) -> OpenLDAP:
    """Installs an OpenLDAP server with TLS configured and returns a reference to it."""
    helm_args = {
        "tls.enabled": "true",
        "tls.secret": openldap_cert,
        # Do not require client certificates
        "env.LDAP_TLS_VERIFY_CLIENT": "never",
    }
    helm_install_from_chart(
        namespace, LDAP_NAME, "stable/openldap", "1.2.4", helm_args=helm_args
    )

    pod = get_pod_when_ready(namespace, LDAP_POD_LABEL)

    yield OpenLDAP(
        ldap_url(namespace, LDAP_PROTO_TLS, LDAP_PORT_TLS),
        ldap_admin_password(namespace),
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
    user = LDAPUser(AUTOMATION_AGENT_NAME, LDAP_PASSWORD,)

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


def ldap_host(namespace: str) -> str:
    return "{}.{}.svc.cluster.local".format(LDAP_NAME, namespace)


def ldap_url(
    namespace: str, proto: str = LDAP_PROTO_PLAIN, port: int = LDAP_PORT_PLAIN
) -> str:
    host = ldap_host(namespace)
    return "{}://{}:{}".format(proto, host, port)


def ldap_admin_password(namespace: str) -> str:
    return KubernetesTester.read_secret(namespace, LDAP_NAME)["LDAP_ADMIN_PASSWORD"]
